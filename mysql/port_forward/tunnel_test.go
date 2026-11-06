package port_forward

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestTunnelCloseListenerOnly(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	tun := &Tunnel{listener: l}
	if err := tun.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Error("listener still accepting")
	}
	// Double close is a no-op.
	if err := tun.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestTunnelCloseNilSafe(t *testing.T) {
	var tun *Tunnel
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (&Tunnel{}).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelCloseOrderAndErrors(t *testing.T) {
	var order []string
	tun := &Tunnel{
		killProxy:    func() error { order = append(order, "proxy"); return errors.New("kill failed") },
		closeSession: func() error { order = append(order, "session"); return errors.New("terminate failed") },
	}
	err := tun.Close()
	if strings.Join(order, ",") != "proxy,session" {
		t.Errorf("order = %v", order)
	}
	if err == nil || !strings.Contains(err.Error(), "kill failed") || !strings.Contains(err.Error(), "terminate failed") {
		t.Errorf("want both errors, got %v", err)
	}
	if err2 := tun.Close(); err2 == nil || len(order) != 2 {
		t.Errorf("second Close must not rerun steps: %v %v", err2, order)
	}
}
