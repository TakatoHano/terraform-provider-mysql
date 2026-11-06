package port_forward

import (
	"net"
	"sync"

	"github.com/hashicorp/go-multierror"
	"golang.org/x/crypto/ssh"
)

// Tunnel owns every resource opened to forward a local port to the database:
// the local listener, the ssh client and, for SSM, the session-manager-plugin
// process and the SSM session. All members are optional.
type Tunnel struct {
	listener net.Listener
	client   *ssh.Client

	killProxy    func() error
	closeSession func() error

	once sync.Once
	err  error
}

// Close releases everything in reverse order of creation: listener, ssh
// client, proxy process, SSM session. It tolerates nil members and is safe to
// call more than once; later calls return the first call's result.
func (t *Tunnel) Close() error {
	if t == nil {
		return nil
	}
	t.once.Do(func() {
		var errs error
		if t.listener != nil {
			if err := t.listener.Close(); err != nil {
				errs = multierror.Append(errs, err)
			}
		}
		if t.client != nil {
			if err := t.client.Close(); err != nil {
				errs = multierror.Append(errs, err)
			}
		}
		if t.killProxy != nil {
			if err := t.killProxy(); err != nil {
				errs = multierror.Append(errs, err)
			}
		}
		if t.closeSession != nil {
			if err := t.closeSession(); err != nil {
				errs = multierror.Append(errs, err)
			}
		}
		t.err = errs
	})
	return t.err
}
