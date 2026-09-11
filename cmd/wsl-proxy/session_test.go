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

func TestSessionDialerIgnoresStaleSessionClear(t *testing.T) {
	dialer := newSessionDialer()
	oldClient := &fakeSessionClient{}
	newClient := &fakeSessionClient{}
	dialer.set(oldClient)
	dialer.set(newClient)
	dialer.clear(oldClient)
	current, _ := dialer.current()
	if current != newClient {
		t.Fatalf("current client=%p, want %p", current, newClient)
	}
}

func TestSessionDialerRetriesPacketAfterSessionReplacement(t *testing.T) {
	oldClient := &fakeSessionClient{packetErr: relay.ErrClientClosed, called: make(chan struct{}, 1)}
	newClient, packet := newFakePacketClient(t)
	dialer := newSessionDialer()
	dialer.set(oldClient)

	type result struct {
		packet net.PacketConn
		err    error
	}
	done := make(chan result, 1)
	go func() {
		value, err := dialer.OpenPacketContext(context.Background())
		done <- result{packet: value, err: err}
	}()
	select {
	case <-oldClient.called:
	case <-time.After(time.Second):
		t.Fatal("old packet session was not used")
	}
	dialer.clear(oldClient)
	dialer.set(newClient)
	select {
	case got := <-done:
		if got.err != nil || got.packet == nil {
			t.Fatalf("result=%+v", got)
		}
		_ = got.packet.Close()
	case <-time.After(time.Second):
		t.Fatal("packet dialer did not use replacement session")
	}
	_ = packet.Close()
}

func TestSessionRetryErrorIncludesClosedNetwork(t *testing.T) {
	if !isSessionRetryError(net.ErrClosed) {
		t.Fatal("net.ErrClosed should trigger session retry")
	}
}

type fakeSessionClient struct {
	dialErr   error
	packetErr error
	conn      net.Conn
	packet    net.PacketConn
	called    chan struct{}
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
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if f.packetErr != nil {
		return nil, f.packetErr
	}
	if f.packet == nil {
		return nil, errors.New("packet test client not implemented")
	}
	return f.packet, nil
}

func newFakePacketClient(t *testing.T) (*fakeSessionClient, net.PacketConn) {
	t.Helper()
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSessionClient{packet: packet, called: make(chan struct{}, 1)}, packet
}
