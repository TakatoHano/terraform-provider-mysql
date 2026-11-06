package port_forward

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startEchoServer starts a TCP echo server on 127.0.0.1 and returns its
// address and a function that stops it (closing the listener and all conns).
func startEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	var closed bool
	stop := func() {
		if !closed {
			closed = true
			l.Close()
		}
	}
	t.Cleanup(stop)
	return l.Addr().String(), stop
}

// startSSHServer starts an in-process SSH server that accepts any client and
// serves direct-tcpip channels by dialing the requested destination.
func startSSHServer(t *testing.T) (addr string, hostKey ssh.PublicKey) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(t, nc, cfg)
		}
	}()

	return l.Addr().String(), signer.PublicKey()
}

func serveSSHConn(t *testing.T, nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)

	for nch := range chans {
		if nch.ChannelType() != "direct-tcpip" {
			_ = nch.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		var p struct {
			DestAddr string
			DestPort uint32
			OrigAddr string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
			_ = nch.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		go func(nch ssh.NewChannel) {
			dst, err := net.Dial("tcp", net.JoinHostPort(p.DestAddr, itoa(p.DestPort)))
			if err != nil {
				_ = nch.Reject(ssh.ConnectionFailed, err.Error())
				return
			}
			ch, chReqs, err := nch.Accept()
			if err != nil {
				dst.Close()
				return
			}
			go ssh.DiscardRequests(chReqs)
			go func() {
				defer ch.Close()
				defer dst.Close()
				_, _ = io.Copy(ch, dst)
			}()
			go func() {
				defer ch.Close()
				defer dst.Close()
				_, _ = io.Copy(dst, ch)
			}()
		}(nch)
	}
}

func itoa(n uint32) string { return strconv.FormatUint(uint64(n), 10) }
