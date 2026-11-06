package port_forward

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func pfTestResource() *schema.Resource {
	return &schema.Resource{
		Schema: map[string]*schema.Schema{
			"port_forward_client_config": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"remote_host":  {Type: schema.TypeString, Required: true},
						"db_endpoint":  {Type: schema.TypeString, Required: true},
						"ssh_user":     {Type: schema.TypeString, Optional: true},
						"ssh_key_path": {Type: schema.TypeString, Optional: true},
					},
				},
			},
		},
	}
}

// setupHome points HOME at a temp dir with ~/.ssh and returns the home dir.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeFile(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, priv
}

func TestParsePFConfigMap(t *testing.T) {
	home := setupHome(t)
	t.Setenv("USER", "testuser")
	defaultKey := filepath.Join(home, ".ssh", "id_rsa")
	writeFile(t, defaultKey, []byte("dummy"))

	res := pfTestResource()
	get := func(raw map[string]interface{}) *schema.ResourceData {
		return schema.TestResourceDataRaw(t, res.Schema, raw)
	}

	t.Run("absent", func(t *testing.T) {
		m, err := ParsePFConfigMap(get(map[string]interface{}{}))
		if m != nil || err != nil {
			t.Fatalf("got %v, %v", m, err)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		m, err := ParsePFConfigMap(get(map[string]interface{}{
			"port_forward_client_config": []interface{}{
				map[string]interface{}{"remote_host": "bastion.example.com", "db_endpoint": "db:3306"},
			},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if m["remote_endpoint"] != "bastion.example.com:22" {
			t.Errorf("remote_endpoint = %q", m["remote_endpoint"])
		}
		if m["db_endpoint"] != "db:3306" {
			t.Errorf("db_endpoint = %q", m["db_endpoint"])
		}
		if m["ssh_user"] == "" {
			t.Errorf("ssh_user default not applied")
		}
		if m["ssh_key_path"] != defaultKey {
			t.Errorf("ssh_key_path = %q, want %q", m["ssh_key_path"], defaultKey)
		}

		conf, err := ParsePFConfig(m, 13306)
		if err != nil {
			t.Fatal(err)
		}
		if conf.localPort != 13306 || conf.remoteEndpoint != "bastion.example.com:22" || conf.dbEndpoint != "db:3306" {
			t.Errorf("unexpected conf: %+v", conf)
		}
	})

	t.Run("explicit", func(t *testing.T) {
		custom := filepath.Join(home, "custom_key")
		writeFile(t, custom, []byte("dummy"))
		m, err := ParsePFConfigMap(get(map[string]interface{}{
			"port_forward_client_config": []interface{}{
				map[string]interface{}{
					"remote_host":  "h",
					"db_endpoint":  "d:1",
					"ssh_user":     "alice",
					"ssh_key_path": custom,
				},
			},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if m["ssh_user"] != "alice" || m["ssh_key_path"] != custom {
			t.Errorf("explicit values not honoured: %v", m)
		}
		conf, err := ParsePFConfig(m, 1)
		if err != nil {
			t.Fatal(err)
		}
		if conf.sshUser != "alice" || conf.keyPath != custom {
			t.Errorf("conf = %+v", conf)
		}
	})

	t.Run("missing key file", func(t *testing.T) {
		missing := filepath.Join(home, "nope")
		_, err := ParsePFConfig(map[string]string{
			"remote_endpoint": "h:22",
			"db_endpoint":     "d:1",
			"ssh_user":        "alice",
			"ssh_key_path":    missing,
		}, 1)
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("expected error mentioning %q, got %v", missing, err)
		}
	})

	t.Run("nil map", func(t *testing.T) {
		c, err := ParsePFConfig(nil, 1)
		if c != nil || err != nil {
			t.Fatalf("got %v, %v", c, err)
		}
	})

	t.Run("empty map", func(t *testing.T) {
		if _, err := ParsePFConfig(map[string]string{}, 1); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestCurrentUsernameAndDefaultSSHKeyPath(t *testing.T) {
	if currentUsername() == "" {
		t.Error("currentUsername returned empty")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if got, want := defaultSSHKeyPath(), filepath.Join(tmp, ".ssh", "id_rsa"); got != want {
		t.Errorf("defaultSSHKeyPath = %q, want %q", got, want)
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func TestCreateHostKeyCallback(t *testing.T) {
	home := setupHome(t)
	kh := filepath.Join(home, ".ssh", "known_hosts")
	writeFile(t, kh, nil)

	cb, err := createHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := newSigner(t)
	s2, _ := newSigner(t)
	host := "127.0.0.1:2222"
	remote := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}

	if err := cb(host, remote, s1.PublicKey()); err != nil {
		t.Fatalf("first use: %v", err)
	}
	after1, _ := os.ReadFile(kh)
	if !strings.Contains(string(after1), "ssh-ed25519") || strings.Count(string(after1), "\n") != 1 {
		t.Fatalf("known_hosts not written as expected: %q", after1)
	}
	if !strings.Contains(string(after1), knownhosts.Normalize(remote.String())) {
		t.Fatalf("known_hosts has no entry for host: %q", after1)
	}

	// knownhosts.New snapshots the file, so a repeat connection (which builds
	// a fresh callback, as CreateSSHClientConfig does) must not append again.
	cb, err = createHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	if err := cb(host, remote, s1.PublicKey()); err != nil {
		t.Fatalf("second use: %v", err)
	}
	after2, _ := os.ReadFile(kh)
	if string(after1) != string(after2) {
		t.Fatalf("known_hosts changed on repeat: %q -> %q", after1, after2)
	}

	err = cb(host, remote, s2.PublicKey())
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) || len(ke.Want) == 0 {
		t.Fatalf("expected KeyError with Want, got %v", err)
	}

	// pipe branch: hostname is used for the written line.
	if err := cb("pipehost.example:22", pipeAddr{}, s1.PublicKey()); err != nil {
		t.Fatalf("pipe: %v", err)
	}
	after3, _ := os.ReadFile(kh)
	if !strings.Contains(string(after3), knownhosts.Normalize("pipehost.example:22")) {
		t.Fatalf("pipe host not recorded: %q", after3)
	}
}

func TestCreateSSHClientConfig(t *testing.T) {
	home := setupHome(t)
	writeFile(t, filepath.Join(home, ".ssh", "known_hosts"), nil)

	_, priv := newSigner(t)
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, "key")
	writeFile(t, keyPath, pem.EncodeToMemory(blk))

	cfg, err := (&portFowardConfig{sshUser: "bob", keyPath: keyPath}).CreateSSHClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "bob" || len(cfg.Auth) != 1 || cfg.HostKeyCallback == nil {
		t.Errorf("unexpected config: %+v", cfg)
	}

	if _, err := (&portFowardConfig{sshUser: "bob", keyPath: filepath.Join(home, "missing")}).CreateSSHClientConfig(); err == nil {
		t.Error("expected error for missing key")
	}
	garbage := filepath.Join(home, "garbage")
	writeFile(t, garbage, []byte("not a key"))
	if _, err := (&portFowardConfig{sshUser: "bob", keyPath: garbage}).CreateSSHClientConfig(); err == nil {
		t.Error("expected error for garbage key")
	}
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

func echoRoundTrip(t *testing.T, addr, msg string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := readFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("got %q, want %q", buf, msg)
	}
}

func readFull(c net.Conn, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := c.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestPortForwardEndToEnd(t *testing.T) {
	sshAddr, _ := startSSHServer(t)
	echoAddr, stopEcho := startEchoServer(t)

	port := freePort(t)
	pfConf := &portFowardConfig{localPort: port, dbEndpoint: echoAddr}

	client, err := ssh.Dial("tcp", sshAddr, &ssh.ClientConfig{
		User:            "u",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	if err := pfConf.PortForward(client); err != nil {
		t.Fatal(err)
	}
	local := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))

	echoRoundTrip(t, local, "hello")
	echoRoundTrip(t, local, "second connection")

	// Break the DB side.
	stopEcho()

	c3, err := net.DialTimeout("tcp", local, 2*time.Second)
	if err != nil {
		t.Fatalf("third dial: %v", err)
	}
	_ = c3.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c3.Read(make([]byte, 1)); err == nil {
		t.Error("expected read error after failed upstream dial")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Errorf("connection was not closed promptly: %v", err)
	}
	c3.Close()

	// Listener must still accept.
	c4, err := net.DialTimeout("tcp", local, 2*time.Second)
	if err != nil {
		t.Fatalf("listener stopped accepting after dial failure: %v", err)
	}
	c4.Close()
}
