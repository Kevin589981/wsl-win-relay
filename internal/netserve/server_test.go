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

func TestServeWithLimitRejectsExcessAndRecoversCapacity(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan net.Conn, 4)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- ServeWithLimit(ctx, listener, 1, func(_ context.Context, conn net.Conn) {
			entered <- conn
			<-release
		})
	}()
	first, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first handler did not start")
	}
	second, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatal("excess connection was not closed")
	}
	_ = second.Close()
	close(release)

	deadline := time.Now().Add(time.Second)
	acceptedAfterRelease := false
	for time.Now().Before(deadline) && !acceptedAfterRelease {
		candidate, dialErr := net.Dial("tcp", listener.Addr().String())
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		select {
		case <-entered:
			acceptedAfterRelease = true
		case <-time.After(10 * time.Millisecond):
		}
		_ = candidate.Close()
	}
	if !acceptedAfterRelease {
		t.Fatal("connection capacity was not restored")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeWithLimitRejectsInvalidLimit(t *testing.T) {
	if err := ServeWithLimit(context.Background(), &nilListener{}, 0, func(context.Context, net.Conn) {}); err == nil {
		t.Fatal("zero connection limit was accepted")
	}
}

type nilListener struct{}

func (*nilListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*nilListener) Close() error              { return nil }
func (*nilListener) Addr() net.Addr            { return nil }
