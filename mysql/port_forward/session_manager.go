package port_forward

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
)

type sessionConfig struct {
	instanceID string
	profile    string
	session    *session.Session
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

	sessionConf.session, _ = session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable, // Must be set to enable
		Profile:           profile,
		Config:            aws.Config{Region: aws.String(region)},
	})

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

func Connect(sessConf *sessionConfig, pfConf *portFowardConfig) error {
	if pfConf == nil {
		return nil
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
	if conf.session == nil {
		errors = multierror.Append(errors, fmt.Errorf("AWS configure is not a valid"))
	}

	if errors != nil {
		return errors
	}

	return nil
}

func (conf *sessionConfig) connect(pfConf *portFowardConfig) error {
	proxyCmd, closeSession, err := openSession(ssm.New(conf.session), conf.instanceID, conf.profile)
	if err != nil {
		return err
	}

	sshConfig, err := pfConf.CreateSSHClientConfig()
	if err != nil {
		errors := err

		if err := closeSession(); err != nil {
			errors = multierror.Append(errors, err)
		}
		return errors
	}

	sshClient, killProxyCmd, err := pfConf.CreateSSHClientWithProxyCommand(proxyCmd, sshConfig)
	if err != nil {
		errors := err

		if err := closeSession(); err != nil {
			errors = multierror.Append(errors, err)
		}
		return errors
	}

	if err := pfConf.PortForward(sshClient); err != nil {
		errors := err

		if err := sshClient.Close(); err != nil {
			errors = multierror.Append(errors, err)
		}
		if err := killProxyCmd(); err != nil {
			errors = multierror.Append(errors, err)
		}
		if err := closeSession(); err != nil {
			errors = multierror.Append(errors, err)
		}
		return errors
	}

	return nil
}

func openSession(svc *ssm.SSM, instanceID, profile string) (*exec.Cmd, func() error, error) {
	in := &ssm.StartSessionInput{
		DocumentName: aws.String("AWS-StartSSHSession"),
		Parameters: map[string][]*string{
			"portNumber": {aws.String("22")},
		},
		Target: aws.String(instanceID),
	}
	out, err := svc.StartSession(in)
	if err != nil {
		return nil, nil, err
	}

	close := func() error {
		in := &ssm.TerminateSessionInput{
			SessionId: out.SessionId,
		}
		if _, err := svc.TerminateSession(in); err != nil {
			return err
		}
		return nil
	}

	cmd, err := sessionManagerPlugin(svc, in, out, profile)
	if err != nil {
		defer close()
		return nil, nil, err
	}

	return cmd, close, nil
}

func sessionManagerPlugin(
	svc *ssm.SSM,
	in *ssm.StartSessionInput,
	out *ssm.StartSessionOutput,
	profile string,
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
	region := *svc.Config.Region
	if profile == "" {
		profile = getAWSProfile()
	}
	endpoint := svc.Endpoint

	cmd := exec.Command(command, string(encodedOut), region, "StartSession", profile, string(encodedIn), endpoint)

	return cmd, nil
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
