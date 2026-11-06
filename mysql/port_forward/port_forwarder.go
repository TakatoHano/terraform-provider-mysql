package port_forward

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path"
	"sync"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type portFowardConfig struct {
	sshUser        string
	keyPath        string
	localPort      uint16
	remoteEndpoint string
	dbEndpoint     string
}

func ParsePFConfigMap(d *schema.ResourceData) (map[string]string, error) {
	v, ok := d.GetOk("port_forward_client_config")
	if !ok {
		return nil, nil
	}

	if !(len(v.([]interface{})) > 0 && v.([]interface{})[0] != nil) {
		return nil, fmt.Errorf("portForwardConfig's format validate")
	}

	confMap := v.([]interface{})[0].(map[string]interface{})
	pfConf := map[string]string{}

	if v, ok := confMap["remote_host"].(string); ok && v != "" {
		addr := fmt.Sprintf("%s:22", v)
		pfConf["remote_endpoint"] = addr
	}

	if v, ok := confMap["db_endpoint"].(string); ok && v != "" {
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

	return pfConf, nil

}

func ParsePFConfig(confMap map[string]string, localPort uint16) (*portFowardConfig, error) {

	if confMap == nil {
		return nil, nil
	}

	if !(len(confMap) > 0) {
		return nil, fmt.Errorf("parseSSHConfig's format validate")
	}
	conf := &portFowardConfig{}

	if v, ok := confMap["remote_endpoint"]; ok && v != "" {
		conf.remoteEndpoint = v
	}

	if v, ok := confMap["db_endpoint"]; ok && v != "" {
		conf.dbEndpoint = v
	}
	conf.sshUser = currentUsername()
	if v, ok := confMap["ssh_user"]; ok && v != "" {
		conf.sshUser = v
	}

	conf.keyPath = defaultSSHKeyPath()
	if v, ok := confMap["ssh_key_path"]; ok && v != "" {
		conf.keyPath = v
	}

	if err := conf.validate(); err != nil {
		return nil, err
	}

	conf.localPort = localPort

	return conf, nil

}

func (pfConf *portFowardConfig) validate() error {

	var errors error

	if pfConf.sshUser == "" {
		errors = multierror.Append(errors, fmt.Errorf("not set ssh_user"))
	}

	if _, err := os.Stat(pfConf.keyPath); err != nil {
		errors = multierror.Append(errors, fmt.Errorf("ssh_key_path: %s is not exist", pfConf.keyPath))
	}

	if errors != nil {
		return errors
	}

	return nil
}

func (pfConf *portFowardConfig) Connect() (*Tunnel, error) {
	if pfConf == nil {
		return nil, nil
	}

	sshConfig, err := pfConf.CreateSSHClientConfig()
	if err != nil {
		return nil, err
	}

	client, err := pfConf.CreateSSHClient(sshConfig)
	if err != nil {
		return nil, err
	}

	t := &Tunnel{client: client}
	listener, err := pfConf.PortForward(client)
	if err != nil {
		return nil, closeAfter(err, t)
	}
	t.listener = listener

	return t, nil
}

// closeAfter closes t and returns err combined with any close errors.
func closeAfter(err error, t *Tunnel) error {
	if cerr := t.Close(); cerr != nil {
		return multierror.Append(err, cerr)
	}
	return err
}

// currentUsername returns the current OS user name, falling back to $USER.
// It returns an empty string when neither is available.
func currentUsername() string {
	if cu, err := user.Current(); err == nil && cu != nil && cu.Username != "" {
		return cu.Username
	}
	return os.Getenv("USER")
}

func defaultSSHKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return path.Join(home, ".ssh", "id_rsa")
}

func (conf *portFowardConfig) CreateSSHClientConfig() (*ssh.ClientConfig, error) {
	key, err := os.ReadFile(conf.keyPath)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := createHostKeyCallback()
	if err != nil {
		return nil, err
	}

	return &ssh.ClientConfig{
		User: conf.sshUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
	}, nil
}

func createHostKeyCallback() (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	knownHosts := path.Join(home, ".ssh", "known_hosts")

	cb, err := knownhosts.New(knownHosts)
	if err != nil {
		return nil, err
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if remote.String() == "pipe" {
			remote = &addrImpl{
				network: remote.Network(),
				addr:    hostname,
			}
		}

		err := cb(hostname, remote, key)

		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			if len(ke.Want) > 0 {
				return ke
			}

			f, err := os.OpenFile(knownHosts, os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return err
			}
			defer f.Close()

			new_host := knownhosts.Line([]string{remote.String()}, key)
			fmt.Fprintln(f, new_host)

			return nil
		}

		return err
	}, nil
}

type addrImpl struct {
	network string
	addr    string
}

func (s *addrImpl) Network() string {
	return s.network
}

func (s *addrImpl) String() string {
	return s.addr
}

func (pfConf *portFowardConfig) CreateSSHClient(
	sshConf *ssh.ClientConfig,
) (*ssh.Client, error) {
	return ssh.Dial("tcp", pfConf.remoteEndpoint, sshConf)
}

func (pfConf *portFowardConfig) CreateSSHClientWithProxyCommand(
	proxyCmd *exec.Cmd,
	sshConf *ssh.ClientConfig,
) (*ssh.Client, func() error, error) {
	c, s := net.Pipe()

	proxyCmd.Stdin = s
	proxyCmd.Stdout = s
	proxyCmd.Stderr = os.Stderr

	if err := proxyCmd.Start(); err != nil {
		return nil, nil, err
	}

	done := func() error {
		// The plugin usually exits on its own once the ssh client closes the pipe.
		if err := proxyCmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return nil
	}

	conn, chans, reqs, err := ssh.NewClientConn(c, pfConf.remoteEndpoint, sshConf)
	if err != nil {
		defer done()
		return nil, nil, err
	}

	client := ssh.NewClient(conn, chans, reqs)

	return client, done, nil
}

// PortForward starts listening on the local port and forwards every accepted
// connection to dbEndpoint through sshClient. The accept loop ends when the
// returned listener is closed.
func (pfConf *portFowardConfig) PortForward(sshClient *ssh.Client) (net.Listener, error) {
	// Bind to loopback only so the tunnel is not exposed to the LAN.
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", pfConf.localPort))
	if err != nil {
		return nil, err
	}

	go func() {
		defer listener.Close()

		for {
			localConn, err := listener.Accept()
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					continue
				}
				if errors.Is(err, net.ErrClosed) {
					return
				}
				fmt.Fprintln(os.Stderr, "accept failed: ", err)
				return
			}

			remoteConn, err := sshClient.Dial("tcp", pfConf.dbEndpoint)
			if err != nil {
				fmt.Fprintln(os.Stderr, "dial failed: ", err)
				localConn.Close()
				continue
			}

			var once sync.Once
			closeBoth := func() {
				once.Do(func() {
					localConn.Close()
					remoteConn.Close()
				})
			}

			go func() {
				defer closeBoth()
				if _, err := io.Copy(remoteConn, localConn); err != nil {
					fmt.Fprintln(os.Stderr, "copy failed: ", err)
				}
			}()

			go func() {
				defer closeBoth()
				if _, err := io.Copy(localConn, remoteConn); err != nil {
					fmt.Fprintln(os.Stderr, "copy failed: ", err)
				}
			}()
		}
	}()

	return listener, nil
}
