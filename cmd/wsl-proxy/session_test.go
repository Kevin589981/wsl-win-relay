package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/relay"
)

func TestSessionDialerWaitsUntilContextCancellation(t *testing.T) {
	dialer := newSessionDialer()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := dialer.DialContext(ctx, "example.com:443"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestSessionDialerRetriesAfterSessionReplacement(t *testing.T) {
	oldClient := &fakeSessionClient{dialErr: relay.ErrClientClosed, called: make(chan struct{}, 1)}
	newClient, peer := newFakeSessionClient()
	defer peer.Close()
	dialer := newSessionDialer()
	dialer.set(oldClient)

	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := dialer.DialContext(context.Background(), "example.com:443")
		done <- result{conn: conn, err: err}
	}()
	select {
	case <-oldClient.called:
	case <-time.After(time.Second):
		t.Fatal("old session was not used")
	}
	dialer.clear(oldClient)
	dialer.set(newClient)
	select {
	case got := <-done:
		if got.err != nil || got.conn == nil {
			t.Fatalf("result=%+v", got)
		}
		_ = got.conn.Close()
	case <-time.After(time.Second):
		t.Fatal("dialer did not use replacement session")
	}
}

type fakeSessionClient struct {
	dialErr error
	conn    net.Conn
	called  chan struct{}
}

func newFakeSessionClient() (*fakeSessionClient, net.Conn) {
	local, peer := net.Pipe()
	return &fakeSessionClient{conn: local}, peer
}

func (f *fakeSessionClient) DialContext(context.Context, string) (net.Conn, error) {
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	return f.conn, nil
}

func (f *fakeSessionClient) OpenPacketContext(context.Context) (net.PacketConn, error) {
	return nil, errors.New("packet test client not implemented")
}
