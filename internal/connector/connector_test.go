package connector

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/localipc"
)

func TestConnectCompletesAttachAndResume(t *testing.T) {
	endpoint := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := localipc.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	registry, err := attach.NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_, _, _, err = attach.ServerResumeHandshake(conn, registry, 0x40, attach.Summary{})
		serverDone <- err
	}()
	session, err := Connect(context.Background(), Config{Endpoint: endpoint, Token: []byte("secret"), Capabilities: 0x12, LastEpoch: 3})
	if err != nil {
		t.Fatal(err)
	}
	if session.Epoch != 1 || session.PeerCapabilities != 0x40 || session.Summary.Epoch != 1 {
		t.Fatalf("session=%+v", session)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestConnectCancellationClosesInProgressHandshake(t *testing.T) {
	endpoint := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := localipc.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = Connect(ctx, Config{Endpoint: endpoint, Token: []byte("secret")})
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	select {
	case conn := <-accepted:
		defer conn.Close()
		buffer := make([]byte, 512)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			if _, readErr := conn.Read(buffer); readErr != nil {
				break
			}
		}
	case <-time.After(time.Second):
		t.Fatal("server did not observe connector connection")
	}
}

func TestBridgeCopiesBothDirections(t *testing.T) {
	left, right := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Bridge(ctx, left, right) }()
	go func() { _, _ = left.Write([]byte("to-right")) }()
	got := make([]byte, 8)
	if _, err := io.ReadFull(right, got); err != nil || string(got) != "to-right" {
		t.Fatalf("right read=%q err=%v", got, err)
	}
	go func() { _, _ = right.Write([]byte("to-left")) }()
	got = make([]byte, 7)
	if _, err := io.ReadFull(left, got); err != nil || string(got) != "to-left" {
		t.Fatalf("left read=%q err=%v", got, err)
	}
	_ = right.Close()
	if err := <-done; err == nil {
		t.Fatal("bridge returned nil after peer close")
	}
}

func TestBridgeRejectsNilEndpoints(t *testing.T) {
	if err := Bridge(context.Background(), nil, &bytes.Buffer{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err=%v", err)
	}
}
