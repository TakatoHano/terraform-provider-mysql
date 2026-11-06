package port_forward

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	helperEnvFlag = "GO_WANT_HELPER_PROCESS"
	helperEnvAddr = "FAKE_PLUGIN_SSH_ADDR"
	helperEnvArgs = "FAKE_PLUGIN_ARGS_FILE"
)

// TestHelperProcess is not a real test: it is the body of the fake
// session-manager-plugin, run as a subprocess of the test binary. It dials the
// in-process SSH server and pipes stdin/stdout to it, as the real plugin pipes
// them to the SSM data channel.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvFlag) != "1" {
		return
	}
	// Record the argv the plugin received (everything after "--").
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if f := os.Getenv(helperEnvArgs); f != "" {
		b, _ := json.Marshal(args)
		_ = os.WriteFile(f, b, 0o600)
	}
	conn, err := net.Dial("tcp", os.Getenv(helperEnvAddr))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake plugin dial:", err)
		os.Exit(2)
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		conn.Close()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	os.Exit(0)
}

// fakeSSMServer implements the two SSM operations over AWS JSON 1.1.
type fakeSSMServer struct {
	*httptest.Server
	mu         sync.Mutex
	starts     int
	terminated []string
}

func newFakeSSMServer(t *testing.T) *fakeSSMServer {
	t.Helper()
	f := &fakeSSMServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Header.Get("X-Amz-Target") {
		case "AmazonSSM.StartSession":
			f.starts++
			_, _ = w.Write([]byte(`{"SessionId":"s-1","TokenValue":"t","StreamUrl":"wss://x"}`))
		case "AmazonSSM.TerminateSession":
			var in struct{ SessionId string }
			_ = json.Unmarshal(body, &in)
			f.terminated = append(f.terminated, in.SessionId)
			_, _ = w.Write([]byte(`{"SessionId":"s-1"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"UnknownOperationException","message":"unknown"}`))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSSMServer) counts() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, append([]string(nil), f.terminated...)
}

// useFakePlugin makes sessionManagerPlugin spawn this test binary as the
// plugin, connected to sshAddr. It returns the file the plugin records its
// argv in.
func useFakePlugin(t *testing.T, sshAddr string) (string, <-chan *exec.Cmd) {
	t.Helper()
	cmds := make(chan *exec.Cmd, 4)
	argsFile := filepath.Join(t.TempDir(), "plugin-args.json")
	orig := newPluginCmd
	newPluginCmd = func(_ string, args []string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), helperEnvFlag+"=1", helperEnvAddr+"="+sshAddr, helperEnvArgs+"="+argsFile)
		cmds <- cmd
		return cmd
	}
	t.Cleanup(func() { newPluginCmd = orig })
	return argsFile, cmds
}

func writeTestKey(t *testing.T, dir string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "key")
	writeFile(t, p, pem.EncodeToMemory(blk))
	return p
}

func newE2EConfig(t *testing.T, fake *fakeSSMServer, instance string) (*sessionConfig, *portFowardConfig, string) {
	t.Helper()
	home := setupOfflineEnv(t)
	echoAddr, _ := startEchoServer(t)
	cfg := aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		BaseEndpoint: aws.String(fake.URL),
		// The fake server never needs retries and failures should be fast.
		RetryMaxAttempts: 1,
	}
	port := freePort(t)
	pf := &portFowardConfig{
		sshUser:        "u",
		keyPath:        writeTestKey(t, home),
		localPort:      port,
		remoteEndpoint: instance + ":22",
		dbEndpoint:     echoAddr,
	}
	sc := &sessionConfig{instanceID: instance, awsConfig: &cfg}
	return sc, pf, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
}

func TestSessionConnectEndToEnd(t *testing.T) {
	fake := newFakeSSMServer(t)
	sshAddr, _ := startSSHServer(t)
	argsFile, cmds := useFakePlugin(t, sshAddr)
	sc, pf, local := newE2EConfig(t, fake, "i-0123")

	tun, err := sc.connect(pf)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if starts, term := fake.counts(); starts != 1 || len(term) != 0 {
		t.Fatalf("after connect: starts=%d terminated=%v", starts, term)
	}

	echoRoundTrip(t, local, "through ssm")
	echoRoundTrip(t, local, "again")

	// The plugin received the argv the real one would.
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("plugin argv not recorded: %v", err)
	}
	var args []string
	if err := json.Unmarshal(b, &args); err != nil || len(args) != 6 {
		t.Fatalf("plugin args = %q (%v)", b, err)
	}
	if args[1] != "us-east-1" || args[2] != "StartSession" || args[5] != fake.URL {
		t.Errorf("unexpected plugin args: %q", args)
	}
	var out struct{ SessionId, TokenValue, StreamUrl string }
	if err := json.Unmarshal([]byte(args[0]), &out); err != nil || out.SessionId != "s-1" || out.TokenValue != "t" || out.StreamUrl != "wss://x" {
		t.Errorf("plugin session arg = %q (%v)", args[0], err)
	}

	if err := tun.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, term := fake.counts(); len(term) != 1 || term[0] != "s-1" {
		t.Errorf("terminate calls = %v, want exactly [s-1]", term)
	}
	if c, err := net.DialTimeout("tcp", local, time.Second); err == nil {
		c.Close()
		t.Error("local port still accepts connections after Close")
	}
	// Tunnel.Close must have killed the fake plugin: Wait returns promptly.
	cmd := <-cmds
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Error("fake plugin still running after Tunnel.Close")
	}
}

func TestSessionConnectSSHRejected(t *testing.T) {
	fake := newFakeSSMServer(t)
	sshAddr, hostKey := startSSHServer(t)
	_, _ = useFakePlugin(t, sshAddr)
	sc, pf, local := newE2EConfig(t, fake, "i-reject")

	// Pin a different host key for the instance so the SSH handshake is
	// rejected by the known_hosts check.
	other, _ := newSigner(t)
	if other.PublicKey().Type() != hostKey.Type() {
		t.Fatal("unexpected key type")
	}
	home, _ := os.UserHomeDir()
	line := knownhosts.Line([]string{pf.remoteEndpoint}, other.PublicKey())
	writeFile(t, filepath.Join(home, ".ssh", "known_hosts"), []byte(line+"\n"))

	tun, err := sc.connect(pf)
	if err == nil || tun != nil {
		t.Fatalf("connect = (%v, %v), want error and nil tunnel", tun, err)
	}
	if starts, term := fake.counts(); starts != 1 || len(term) != 1 || term[0] != "s-1" {
		t.Errorf("starts=%d terminated=%v, want 1 start and exactly [s-1]", starts, term)
	}
	if c, err := net.DialTimeout("tcp", local, time.Second); err == nil {
		c.Close()
		t.Error("local port is listening after failed connect")
	}
}
