package port_forward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
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

const (
	// ModeSSH tunnels SSH over an SSM session (AWS-StartSSHSession).
	ModeSSH = "ssh"
	// ModePortForwarding uses AWS-StartPortForwardingSessionToRemoteHost and
	// needs no SSH.
	ModePortForwarding = "port_forwarding"

	portForwardingDocument = "AWS-StartPortForwardingSessionToRemoteHost"
	defaultRemotePort      = 3306
)

// Readiness polling of the local port opened by session-manager-plugin. They
// are variables so that tests can shorten them.
var (
	pluginReadyTimeout  = 30 * time.Second
	pluginReadyInterval = 200 * time.Millisecond
)

type sessionConfig struct {
	instanceID string
	profile    string
	awsConfig  *aws.Config
	// mode is ModeSSH or ModePortForwarding; empty means ModeSSH.
	mode string
	// remoteHost and remotePort are the RDS endpoint (port_forwarding mode).
	remoteHost string
	remotePort int
}

func (conf *sessionConfig) isPortForwarding() bool {
	return conf != nil && conf.mode == ModePortForwarding
}

// TunnelKey identifies the settings that determine the tunnel, so that a
// later provider configuration with the same settings can reuse it.
func (conf *sessionConfig) TunnelKey() string {
	if conf == nil {
		return ""
	}
	region := ""
	if conf.awsConfig != nil {
		region = conf.awsConfig.Region
	}
	mode := conf.mode
	if mode == "" {
		mode = ModeSSH
	}
	return fmt.Sprintf("%s|%s|%s|%d|%s|%s", mode, conf.instanceID, conf.remoteHost, conf.remotePort, conf.profile, region)
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

	sessionConf := &sessionConfig{mode: ModeSSH}
	if v, ok := confMap["mode"].(string); ok && v != "" {
		sessionConf.mode = v
	}
	if sessionConf.mode != ModeSSH && sessionConf.mode != ModePortForwarding {
		return nil, nil, fmt.Errorf("invalid mode %q: must be %q or %q", sessionConf.mode, ModeSSH, ModePortForwarding)
	}
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

	rdsEndpoint, _ := confMap["rds_endpoint"].(string)

	if sessionConf.mode == ModePortForwarding {
		// No SSH: ssh_user/ssh_key_path are ignored and pfConf stays nil.
		host, port, err := splitRDSEndpoint(rdsEndpoint)
		if err != nil {
			return nil, nil, err
		}
		sessionConf.remoteHost = host
		sessionConf.remotePort = port
		return sessionConf, nil, nil
	}

	if rdsEndpoint != "" {
		pfConf["db_endpoint"] = rdsEndpoint
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

// splitRDSEndpoint parses host[:port], defaulting the port to 3306.
func splitRDSEndpoint(endpoint string) (string, int, error) {
	if endpoint == "" {
		return "", 0, fmt.Errorf("not set rds_endpoint")
	}
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		// No port (or a bare IPv6 literal): use the MySQL default.
		host = strings.TrimSuffix(strings.TrimPrefix(endpoint, "["), "]")
		if host == "" || strings.ContainsAny(host, "/ ") {
			return "", 0, fmt.Errorf("invalid rds_endpoint %q", endpoint)
		}
		return host, defaultRemotePort, nil
	}
	if host == "" {
		return "", 0, fmt.Errorf("invalid rds_endpoint %q: empty host", endpoint)
	}
	if portStr == "" {
		return host, defaultRemotePort, nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid rds_endpoint %q: bad port %q", endpoint, portStr)
	}
	return host, port, nil
}

// Connect opens the configured tunnel. It returns (nil, nil) when no port
// forwarding is configured. The caller owns the returned Tunnel and must Close it.
// localPort is the local port to listen on in port_forwarding mode; in ssh mode
// the port comes from pfConf.
func Connect(sessConf *sessionConfig, pfConf *portFowardConfig, localPort uint16) (*Tunnel, error) {
	if sessConf.isPortForwarding() {
		return sessConf.connectPortForwarding(localPort)
	}
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

	close := terminateFunc(svc, out)

	cmd, err := sessionManagerPlugin(in, out, profile, region, endpoint)
	if err != nil {
		defer close()
		return nil, nil, err
	}

	return cmd, close, nil
}

// terminateFunc returns a func that terminates the SSM session out.
func terminateFunc(svc ssmAPI, out *ssm.StartSessionOutput) func() error {
	return func() error {
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
}

// portForwardingInput builds the StartSession request for port_forwarding mode.
func portForwardingInput(instanceID, host string, remotePort int, localPort uint16) *ssm.StartSessionInput {
	return &ssm.StartSessionInput{
		DocumentName: aws.String(portForwardingDocument),
		Parameters: map[string][]string{
			"host":            {host},
			"portNumber":      {strconv.Itoa(remotePort)},
			"localPortNumber": {strconv.Itoa(int(localPort))},
		},
		Target: aws.String(instanceID),
	}
}

func (conf *sessionConfig) connectPortForwarding(localPort uint16) (*Tunnel, error) {
	ctx := context.Background()
	endpoint, err := resolveSSMEndpoint(ctx, *conf.awsConfig)
	if err != nil {
		return nil, err
	}
	return openPortForwardingSession(ctx, newSSMClient(*conf.awsConfig), conf, localPort, endpoint)
}

// logWriter forwards the plugin's output to the provider log.
type logWriter struct{ stream string }

func (w logWriter) Write(p []byte) (int, error) {
	log.Printf("[DEBUG] session-manager-plugin %s: %s", w.stream, strings.TrimRight(string(p), "\r\n"))
	return len(p), nil
}

// openPortForwardingSession starts an AWS-StartPortForwardingSessionToRemoteHost
// session and the session-manager-plugin, which itself listens on
// 127.0.0.1:localPort. It returns once that port accepts connections. The
// plugin's stdout and stderr go to the provider's debug log.
func openPortForwardingSession(ctx context.Context, svc ssmAPI, conf *sessionConfig, localPort uint16, endpoint string) (*Tunnel, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(localPort)))
	// Something already listening would make the readiness check pass
	// falsely and the plugin fail to bind.
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("local port %d is already in use", localPort)
	}

	in := portForwardingInput(conf.instanceID, conf.remoteHost, conf.remotePort, localPort)
	out, err := svc.StartSession(ctx, in)
	if err != nil {
		return nil, err
	}
	terminate := terminateFunc(svc, out)

	cmd, err := sessionManagerPlugin(in, out, conf.profile, conf.awsConfig.Region, endpoint)
	if err != nil {
		_ = terminate()
		return nil, err
	}
	cmd.Stdout = logWriter{"stdout"}
	cmd.Stderr = logWriter{"stderr"}
	if err := cmd.Start(); err != nil {
		_ = terminate()
		return nil, fmt.Errorf("start session-manager-plugin: %w", err)
	}

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	kill := func() error {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-exited
		return nil
	}

	fail := func(err error) (*Tunnel, error) {
		_ = kill()
		_ = terminate()
		return nil, err
	}
	notReady := fmt.Errorf("session-manager-plugin did not open local port %d within %s; is the plugin installed and is the instance SSM-managed?", localPort, pluginReadyTimeout)

	deadline := time.NewTimer(pluginReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pluginReadyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-exited:
			return fail(notReady)
		default:
		}
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return &Tunnel{killProxy: kill, closeSession: terminate}, nil
		}
		select {
		case <-exited:
			return fail(notReady)
		case <-deadline.C:
			return fail(notReady)
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-ticker.C:
		}
	}
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
