package port_forward

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// newSSMClient builds the SSM API client used by connect. It is a variable so
// that tests can inject a mock.
var newSSMClient = func(cfg aws.Config) ssmAPI { return ssm.NewFromConfig(cfg) }

// ssmAPI is the subset of the SSM client used by this package.
type ssmAPI interface {
	StartSession(ctx context.Context, in *ssm.StartSessionInput, optFns ...func(*ssm.Options)) (*ssm.StartSessionOutput, error)
	TerminateSession(ctx context.Context, in *ssm.TerminateSessionInput, optFns ...func(*ssm.Options)) (*ssm.TerminateSessionOutput, error)
}

type sessionConfig struct {
	instanceID string
	profile    string
	awsConfig  *aws.Config
}

func ParseSessionConfig(d *schema.ResourceData) (*sessionConfig, map[string]string, error) {
	v, ok := d.GetOk("aws_ssm_session_manager_client_config")
	if !ok {
		return nil, nil, nil
	}

	if !(len(v.([]interface{})) > 0 && v.([]interface{})[0] != nil) {
		return nil, nil, fmt.Errorf("parseSessionConfig's format validate")
	}

	confMap := v.([]interface{})[0].(map[string]interface{})
	pfConf := map[string]string{}

	sessionConf := &sessionConfig{}
	if v, ok := confMap["ec2_instance_id"].(string); ok && v != "" {
		sessionConf.instanceID = v
	}
	pfConf["remote_endpoint"] = fmt.Sprintf("%s:22", sessionConf.instanceID)

	profile := ""
	if v, ok := confMap["aws_profile"].(string); ok && v != "" {
		profile = v
	}

	sessionConf.profile = profile

	region := ""
	if v, ok := confMap["region"].(string); ok && v != "" {
		region = v
	}

	// Only pass profile/region when set so environment and shared-config
	// defaults keep working.
	var loadOpts []func(*config.LoadOptions) error
	if profile != "" {
		loadOpts = append(loadOpts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		loadOpts = append(loadOpts, config.WithRegion(region))
	}
	awsCfg, err := config.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("load AWS config: %w", err)
	}
	sessionConf.awsConfig = &awsCfg

	if err := sessionConf.validate(); err != nil {
		return nil, nil, err
	}

	if v, ok := confMap["rds_endpoint"].(string); ok && v != "" {
		pfConf["db_endpoint"] = v
	}

	pfConf["ssh_user"] = currentUsername()
	if v, ok := confMap["ssh_user"].(string); ok && v != "" {
		pfConf["ssh_user"] = v
	}

	pfConf["ssh_key_path"] = defaultSSHKeyPath()
	if v, ok := confMap["ssh_key_path"].(string); ok && v != "" {
		pfConf["ssh_key_path"] = v
	}

	return sessionConf, pfConf, nil

}

// Connect opens the configured tunnel. It returns (nil, nil) when no port
// forwarding is configured. The caller owns the returned Tunnel and must Close it.
func Connect(sessConf *sessionConfig, pfConf *portFowardConfig) (*Tunnel, error) {
	if pfConf == nil {
		return nil, nil
	}
	if sessConf == nil {
		return pfConf.Connect()
	}
	return sessConf.connect(pfConf)
}

func (conf *sessionConfig) validate() error {

	var errors error
	if conf.instanceID == "" {
		errors = multierror.Append(errors, fmt.Errorf("not set ec2_instance_id"))
	}
	if conf.awsConfig == nil {
		errors = multierror.Append(errors, fmt.Errorf("AWS configure is not a valid"))
	}

	if errors != nil {
		return errors
	}

	return nil
}

func (conf *sessionConfig) connect(pfConf *portFowardConfig) (*Tunnel, error) {
	// Region and endpoint are resolved once from the config so openSession
	// only needs the interface.
	ctx := context.Background()
	region := conf.awsConfig.Region
	endpoint, err := resolveSSMEndpoint(ctx, *conf.awsConfig)
	if err != nil {
		return nil, err
	}
	proxyCmd, closeSession, err := openSession(ctx, newSSMClient(*conf.awsConfig), conf.instanceID, conf.profile, region, endpoint)
	if err != nil {
		return nil, err
	}
	t := &Tunnel{closeSession: closeSession}

	sshConfig, err := pfConf.CreateSSHClientConfig()
	if err != nil {
		return nil, closeAfter(err, t)
	}

	sshClient, killProxyCmd, err := pfConf.CreateSSHClientWithProxyCommand(proxyCmd, sshConfig)
	if err != nil {
		return nil, closeAfter(err, t)
	}
	t.client = sshClient
	t.killProxy = killProxyCmd

	listener, err := pfConf.PortForward(sshClient)
	if err != nil {
		return nil, closeAfter(err, t)
	}
	t.listener = listener

	return t, nil
}

// resolveSSMEndpoint returns the SSM endpoint URL for cfg's region, honoring a
// custom base endpoint when configured.
func resolveSSMEndpoint(ctx context.Context, cfg aws.Config) (string, error) {
	ep, err := ssm.NewDefaultEndpointResolverV2().ResolveEndpoint(ctx, ssm.EndpointParameters{
		Region:   aws.String(cfg.Region),
		Endpoint: cfg.BaseEndpoint,
	})
	if err != nil {
		return "", fmt.Errorf("resolve SSM endpoint: %w", err)
	}
	return ep.URI.String(), nil
}

func openSession(ctx context.Context, svc ssmAPI, instanceID, profile, region, endpoint string) (*exec.Cmd, func() error, error) {
	in := &ssm.StartSessionInput{
		DocumentName: aws.String("AWS-StartSSHSession"),
		Parameters: map[string][]string{
			"portNumber": {"22"},
		},
		Target: aws.String(instanceID),
	}
	out, err := svc.StartSession(ctx, in)
	if err != nil {
		return nil, nil, err
	}

	close := func() error {
		in := &ssm.TerminateSessionInput{
			SessionId: out.SessionId,
		}
		// Use a fresh context: ctx may be done by the time cleanup runs.
		tctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := svc.TerminateSession(tctx, in); err != nil {
			return err
		}
		return nil
	}

	cmd, err := sessionManagerPlugin(in, out, profile, region, endpoint)
	if err != nil {
		defer close()
		return nil, nil, err
	}

	return cmd, close, nil
}

func sessionManagerPlugin(
	in *ssm.StartSessionInput,
	out *ssm.StartSessionOutput,
	profile, region, endpoint string,
) (*exec.Cmd, error) {
	command := "session-manager-plugin"
	if runtime.GOOS == "windows" {
		command += ".exe"
	}

	encodedIn, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	encodedOut, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if profile == "" {
		profile = getAWSProfile()
	}

	cmd := newPluginCmd(command, []string{string(encodedOut), region, "StartSession", profile, string(encodedIn), endpoint})

	return cmd, nil
}

// newPluginCmd builds the session-manager-plugin command. It is a variable so
// that tests can substitute a fake plugin.
var newPluginCmd = func(command string, args []string) *exec.Cmd {
	return exec.Command(command, args...)
}

func getAWSProfile() string {
	profile := os.Getenv("AWS_PROFILE")
	if profile != "" {
		return profile
	}

	enableSharedConfig, _ := strconv.ParseBool(os.Getenv("AWS_SDK_LOAD_CONFIG"))
	if enableSharedConfig {
		profile = os.Getenv("AWS_DEFAULT_PROFILE")
	}

	return profile
}
