package port_forward

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	//lint:ignore SA1019 aws-sdk-go v1 is deprecated; migrating to v2 is out of scope here
	"github.com/aws/aws-sdk-go/aws"
	//lint:ignore SA1019 aws-sdk-go v1 is deprecated; migrating to v2 is out of scope here
	"github.com/aws/aws-sdk-go/aws/session"
	//lint:ignore SA1019 aws-sdk-go v1 is deprecated; migrating to v2 is out of scope here
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/aws/aws-sdk-go/service/ssm/ssmiface"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// mockSSM implements only the SSM calls used by session_manager.go.
type mockSSM struct {
	ssmiface.SSMAPI

	mu          sync.Mutex
	startErr    error
	startInputs []*ssm.StartSessionInput
	terminated  []string
}

func (m *mockSSM) StartSession(in *ssm.StartSessionInput) (*ssm.StartSessionOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startInputs = append(m.startInputs, in)
	if m.startErr != nil {
		return nil, m.startErr
	}
	return &ssm.StartSessionOutput{
		SessionId:  aws.String("sess-123"),
		TokenValue: aws.String("token-abc"),
		StreamUrl:  aws.String("wss://ssmmessages.example/stream"),
	}, nil
}

func (m *mockSSM) TerminateSession(in *ssm.TerminateSessionInput) (*ssm.TerminateSessionOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminated = append(m.terminated, aws.StringValue(in.SessionId))
	return &ssm.TerminateSessionOutput{SessionId: in.SessionId}, nil
}

func (m *mockSSM) terminateCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.terminated...)
}

func ssmConfigResource() *schema.Resource {
	return &schema.Resource{Schema: map[string]*schema.Schema{
		"aws_ssm_session_manager_client_config": {
			Type:     schema.TypeList,
			Optional: true,
			MaxItems: 1,
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"ec2_instance_id": {Type: schema.TypeString, Required: true},
					"rds_endpoint":    {Type: schema.TypeString, Required: true},
					"ssh_user":        {Type: schema.TypeString, Optional: true},
					"ssh_key_path":    {Type: schema.TypeString, Optional: true},
					"aws_profile": {
						Type: schema.TypeString,
						DefaultFunc: schema.MultiEnvDefaultFunc([]string{
							"AWS_PROFILE",
							"AWS_DEFAULT_PROFILE",
						}, ""),
						Optional: true,
					},
					"region": {
						Type: schema.TypeString,
						DefaultFunc: schema.MultiEnvDefaultFunc([]string{
							"AWS_REGION",
							"AWS_DEFAULT_REGION",
						}, ""),
						Optional: true,
					},
				},
			},
		},
	}}
}

// setupOfflineEnv isolates the environment so no real AWS config or network is used.
func setupOfflineEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("USER", "tester")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_SDK_LOAD_CONFIG", "false")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_rsa"), []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestParseSessionConfig(t *testing.T) {
	home := setupOfflineEnv(t)
	res := ssmConfigResource()
	key := "aws_ssm_session_manager_client_config"

	t.Run("block absent", func(t *testing.T) {
		d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{})
		sc, pf, err := ParseSessionConfig(d)
		if sc != nil || pf != nil || err != nil {
			t.Fatalf("want (nil,nil,nil), got (%v,%v,%v)", sc, pf, err)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
			key: []interface{}{map[string]interface{}{
				"ec2_instance_id": "i-0123",
				"rds_endpoint":    "db.example:3306",
				"aws_profile":     "prof",
			}},
		})
		sc, pf, err := ParseSessionConfig(d)
		if err != nil {
			t.Fatal(err)
		}
		if sc.instanceID != "i-0123" || sc.profile != "prof" || sc.session == nil {
			t.Fatalf("unexpected sessionConfig: %+v", sc)
		}
		if pf["remote_endpoint"] != "i-0123:22" {
			t.Errorf("remote_endpoint = %q", pf["remote_endpoint"])
		}
		if pf["db_endpoint"] != "db.example:3306" {
			t.Errorf("db_endpoint = %q", pf["db_endpoint"])
		}
		if pf["ssh_user"] != currentUsername() {
			t.Errorf("ssh_user = %q, want %q", pf["ssh_user"], currentUsername())
		}
		if want := filepath.Join(home, ".ssh", "id_rsa"); pf["ssh_key_path"] != want {
			t.Errorf("ssh_key_path = %q, want %q", pf["ssh_key_path"], want)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
			key: []interface{}{map[string]interface{}{
				"ec2_instance_id": "i-0456",
				"rds_endpoint":    "db2.example:3306",
				"ssh_user":        "ec2-user",
				"ssh_key_path":    "/keys/custom",
				"region":          "ap-northeast-1",
			}},
		})
		sc, pf, err := ParseSessionConfig(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := aws.StringValue(sc.session.Config.Region); got != "ap-northeast-1" {
			t.Errorf("region = %q", got)
		}
		if pf["ssh_user"] != "ec2-user" || pf["ssh_key_path"] != "/keys/custom" {
			t.Errorf("overrides not applied: %v", pf)
		}
	})

	t.Run("missing instance id", func(t *testing.T) {
		d := schema.TestResourceDataRaw(t, res.Schema, map[string]interface{}{
			key: []interface{}{map[string]interface{}{
				"ec2_instance_id": "",
				"rds_endpoint":    "db.example:3306",
			}},
		})
		sc, pf, err := ParseSessionConfig(d)
		if err == nil || !strings.Contains(err.Error(), "ec2_instance_id") {
			t.Fatalf("want ec2_instance_id error, got %v", err)
		}
		if sc != nil || pf != nil {
			t.Fatalf("want nil results on error")
		}
	})
}

func TestSessionConfigValidate(t *testing.T) {
	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String("us-east-1")}))

	if err := (&sessionConfig{instanceID: "i-1", session: sess}).validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	err := (&sessionConfig{session: sess}).validate()
	if err == nil || !strings.Contains(err.Error(), "not set ec2_instance_id") {
		t.Errorf("empty instanceID: %v", err)
	}

	err = (&sessionConfig{instanceID: "i-1"}).validate()
	if err == nil || !strings.Contains(err.Error(), "AWS configure is not a valid") {
		t.Errorf("nil session: %v", err)
	}

	err = (&sessionConfig{}).validate()
	var merr *multierror.Error
	if !errors.As(err, &merr) || len(merr.Errors) != 2 {
		t.Fatalf("want multierror with 2 errors, got %v", err)
	}
}

func TestGetAWSProfile(t *testing.T) {
	t.Run("AWS_PROFILE wins", func(t *testing.T) {
		t.Setenv("AWS_PROFILE", "p1")
		t.Setenv("AWS_SDK_LOAD_CONFIG", "true")
		t.Setenv("AWS_DEFAULT_PROFILE", "p2")
		if got := getAWSProfile(); got != "p1" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("default profile with load config", func(t *testing.T) {
		t.Setenv("AWS_PROFILE", "")
		t.Setenv("AWS_SDK_LOAD_CONFIG", "true")
		t.Setenv("AWS_DEFAULT_PROFILE", "p2")
		if got := getAWSProfile(); got != "p2" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("default profile ignored without load config", func(t *testing.T) {
		t.Setenv("AWS_PROFILE", "")
		t.Setenv("AWS_SDK_LOAD_CONFIG", "false")
		t.Setenv("AWS_DEFAULT_PROFILE", "p2")
		if got := getAWSProfile(); got != "" {
			t.Errorf("got %q", got)
		}
	})
}

func TestSessionManagerPlugin(t *testing.T) {
	in := &ssm.StartSessionInput{
		DocumentName: aws.String("AWS-StartSSHSession"),
		Parameters:   map[string][]*string{"portNumber": {aws.String("22")}},
		Target:       aws.String("i-0123"),
	}
	out := &ssm.StartSessionOutput{
		SessionId:  aws.String("sess-123"),
		TokenValue: aws.String("token-abc"),
		StreamUrl:  aws.String("wss://example/stream"),
	}
	const region = "ap-northeast-1"
	const endpoint = "https://ssm.ap-northeast-1.amazonaws.com"
	bin := "session-manager-plugin"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	t.Run("explicit profile", func(t *testing.T) {
		cmd, err := sessionManagerPlugin(in, out, "myprofile", region, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if len(cmd.Args) != 7 {
			t.Fatalf("args = %v", cmd.Args)
		}
		if cmd.Args[0] != bin || cmd.Args[2] != region || cmd.Args[3] != "StartSession" ||
			cmd.Args[4] != "myprofile" || cmd.Args[6] != endpoint {
			t.Errorf("unexpected args: %v", cmd.Args)
		}
		var gotOut ssm.StartSessionOutput
		if err := json.Unmarshal([]byte(cmd.Args[1]), &gotOut); err != nil {
			t.Fatal(err)
		}
		if aws.StringValue(gotOut.SessionId) != "sess-123" || aws.StringValue(gotOut.TokenValue) != "token-abc" {
			t.Errorf("output did not round-trip: %+v", gotOut)
		}
		var gotIn ssm.StartSessionInput
		if err := json.Unmarshal([]byte(cmd.Args[5]), &gotIn); err != nil {
			t.Fatal(err)
		}
		if aws.StringValue(gotIn.Target) != "i-0123" || aws.StringValue(gotIn.DocumentName) != "AWS-StartSSHSession" {
			t.Errorf("input did not round-trip: %+v", gotIn)
		}
	})

	t.Run("profile fallback", func(t *testing.T) {
		t.Setenv("AWS_PROFILE", "envprof")
		cmd, err := sessionManagerPlugin(in, out, "", region, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Args[4] != "envprof" {
			t.Errorf("profile arg = %q", cmd.Args[4])
		}
	})
}

func TestOpenSession(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	const endpoint = "https://ssm.ap-northeast-1.amazonaws.com"

	t.Run("success", func(t *testing.T) {
		m := &mockSSM{}
		cmd, closeFn, err := openSession(m, "i-0123", "myprofile", "ap-northeast-1", endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if cmd == nil || closeFn == nil {
			t.Fatal("want non-nil cmd and close func")
		}
		if len(m.startInputs) != 1 || aws.StringValue(m.startInputs[0].Target) != "i-0123" ||
			aws.StringValue(m.startInputs[0].DocumentName) != "AWS-StartSSHSession" {
			t.Errorf("unexpected StartSession input: %+v", m.startInputs)
		}
		if got := m.terminateCalls(); len(got) != 0 {
			t.Fatalf("terminate called early: %v", got)
		}
		if err := closeFn(); err != nil {
			t.Fatal(err)
		}
		if got := m.terminateCalls(); len(got) != 1 || got[0] != "sess-123" {
			t.Errorf("terminate calls = %v", got)
		}
	})

	t.Run("start failure", func(t *testing.T) {
		boom := errors.New("boom")
		m := &mockSSM{startErr: boom}
		cmd, closeFn, err := openSession(m, "i-0123", "p", "ap-northeast-1", endpoint)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if cmd != nil || closeFn != nil {
			t.Error("want nil cmd and close func")
		}
		if got := m.terminateCalls(); len(got) != 0 {
			t.Errorf("terminate must not be called: %v", got)
		}
	})
}

func TestConnectTerminatesSessionOnError(t *testing.T) {
	m := &mockSSM{}
	orig := newSSMClient
	newSSMClient = func(*session.Session) ssmiface.SSMAPI { return m }
	t.Cleanup(func() { newSSMClient = orig })

	conf := &sessionConfig{
		instanceID: "i-0123",
		profile:    "p",
		session:    session.Must(session.NewSession(&aws.Config{Region: aws.String("us-east-1")})),
	}
	pf := &portFowardConfig{
		sshUser: "u",
		keyPath: filepath.Join(t.TempDir(), "does-not-exist"),
	}

	if err := conf.connect(pf); err == nil {
		t.Fatal("want error for missing key file")
	}
	if got := m.terminateCalls(); len(got) != 1 || got[0] != "sess-123" {
		t.Errorf("terminate calls = %v, want exactly one for sess-123", got)
	}
}
