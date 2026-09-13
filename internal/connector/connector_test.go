package connector

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/localipc"
)

func TestConnectCompletesAttachAndResume(t *testing.T) {
	endpoint := testEndpoint(t)
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
	endpoint := testEndpoint(t)
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
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, connectErr := Connect(ctx, Config{Endpoint: endpoint, Token: []byte("secret")})
		result <- connectErr
	}()
	select {
	case conn := <-accepted:
		cancel()
		defer conn.Close()
		buffer := make([]byte, 512)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			if _, readErr := conn.Read(buffer); readErr != nil {
				break
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not observe connector connection")
	}
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("connector did not return after cancellation")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestConnectTimesOutStalledHandshake(t *testing.T) {
	endpoint := testEndpoint(t)
	listener, err := localipc.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	oldTimeout := connectorHandshakeTimeout
	connectorHandshakeTimeout = 10 * time.Millisecond
	defer func() { connectorHandshakeTimeout = oldTimeout }()
	started := time.Now()
	_, err = Connect(context.Background(), Config{Endpoint: endpoint, Token: []byte("secret")})
	if err == nil {
		t.Fatal("stalled handshake unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed < 5*time.Millisecond || elapsed > time.Second {
		t.Fatalf("handshake timeout elapsed=%s err=%v", elapsed, err)
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("server did not observe connector connection")
	}
}

func TestConnectWithRetryWaitsForBrokerEndpoint(t *testing.T) {
	endpoint := testEndpoint(t)
	registry, err := attach.NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		time.Sleep(40 * time.Millisecond)
		listener, listenErr := localipc.Listen(endpoint)
		if listenErr != nil {
			serverDone <- listenErr
			return
		}
		defer listener.Close()
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()
		_, _, _, handshakeErr := attach.ServerResumeHandshake(conn, registry, 0, attach.Summary{})
		serverDone <- handshakeErr
	}()
	session, err := connectWithRetry(context.Background(), Config{Endpoint: endpoint, Token: []byte("secret")}, time.Second, 10*time.Millisecond, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_ = session.Close()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestConnectWithRetryDoesNotRetryRejectedAttach(t *testing.T) {
	if isRetryableConnectError(attach.ErrRemoteAttach) {
		t.Fatal("remote attach rejection must be terminal")
	}
	if isRetryableConnectError(ErrInvalidConfig) {
		t.Fatal("invalid configuration must be terminal")
	}
	if !isRetryableConnectError(errors.New("endpoint unavailable")) {
		t.Fatal("transport failure must be retryable")
	}
}

func testEndpoint(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return "wsl-win-relay-connector-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	}
	return filepath.Join(t.TempDir(), "broker.sock")
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
