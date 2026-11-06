package port_forward

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
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

	cu, _ := user.Current()
	pfConf["ssh_user"] = cu.Username
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
	cu, _ := user.Current()
	conf.sshUser = cu.Username
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

func (pfConf *portFowardConfig) Connect() error {
	if pfConf == nil {
		return nil
	}

	sshConfig, err := pfConf.CreateSSHClientConfig()
	if err != nil {
		return err
	}

	client, err := pfConf.CreateSSHClient(sshConfig)
	if err != nil {
		return err
	}

	if err := pfConf.PortForward(client); err != nil {
		return err
	}

	return nil
}

func defaultSSHKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return path.Join(home, ".ssh", "id_rsa")
}

func (conf *portFowardConfig) CreateSSHClientConfig() (*ssh.ClientConfig, error) {
	key, err := ioutil.ReadFile(conf.keyPath)
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
		return proxyCmd.Process.Kill()
	}

	conn, chans, reqs, err := ssh.NewClientConn(c, pfConf.remoteEndpoint, sshConf)
	if err != nil {
		defer done()
		return nil, nil, err
	}

	client := ssh.NewClient(conn, chans, reqs)

	return client, done, nil
}

func (pfConf *portFowardConfig) PortForward(sshClient *ssh.Client) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", pfConf.localPort))
	if err != nil {
		return err
	}

	done := make(chan struct{})

	go func() {
		defer listener.Close()

		for {
			select {
			case <-done:
				return
			default:
			}

			localConn, err := listener.Accept()
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					continue
				}
				fmt.Fprintln(os.Stderr, "accept failed: ", err)
				return
			}

			remoteConn, err := sshClient.Dial("tcp", pfConf.dbEndpoint)
			if err != nil {
				fmt.Fprintln(os.Stderr, "dial failed: ", err)
				return
			}

			go func() {
				defer localConn.Close()
				defer remoteConn.Close()
				if _, err := io.Copy(remoteConn, localConn); err != nil {
					fmt.Fprintln(os.Stderr, "copy failed: ", err)
				}
			}()

			go func() {
				if _, err := io.Copy(localConn, remoteConn); err != nil {
					fmt.Fprintln(os.Stderr, "copy failed: ", err)
				}
			}()
		}
	}()

	return nil
}
