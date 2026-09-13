package netserve

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestServeClosesAndDrainsAcceptedConnections(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, listener, func(_ context.Context, conn net.Conn) {
			close(started)
			var probe [1]byte
			_, _ = conn.Read(probe[:])
		})
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not drain handler")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var probe [1]byte
	if _, err := conn.Read(probe[:]); err == nil {
		t.Fatal("accepted connection remained open")
	}
}

func TestServeRejectsNilInputs(t *testing.T) {
	if err := Serve(context.Background(), nil, func(context.Context, net.Conn) {}); err == nil {
		t.Fatal("nil listener was accepted")
	}
}
