package httpproxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type echoDialer struct{}

func (echoDialer) DialContext(context.Context, string) (net.Conn, error) {
	local, remote := net.Pipe()
	go func() { _, _ = io.Copy(remote, remote); _ = remote.Close() }()
	return local, nil
}

func TestConnectTimesOutStalledHandshake(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		done <- (&Server{Dialer: echoDialer{}, HandshakeTimeout: 10 * time.Millisecond}).ServeConn(context.Background(), server)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled HTTP CONNECT handshake unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stalled HTTP CONNECT handshake did not time out")
	}
}

func TestHandshakeReaderBoundsHTTPHeaders(t *testing.T) {
	reader := &handshakeReader{reader: bytes.NewReader(make([]byte, maxConnectRequestBytes+1)), remaining: maxConnectRequestBytes, bounded: true}
	if _, err := io.Copy(io.Discard, reader); err == nil {
		t.Fatal("oversized HTTP CONNECT request was accepted")
	}
	reader.bounded = false
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("unbounded tunnel read failed: %v", err)
	}
}

func TestConnectTimesOutWaitingForRelay(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- (&Server{Dialer: blockingHTTPDialer{}, DialTimeout: 20 * time.Millisecond}).ServeConn(context.Background(), server)
	}()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(client, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 64)
	count, err := client.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(response[:count]), "HTTP/1.1 502") {
		t.Fatalf("response %q", response[:count])
	}
	_ = client.Close()
	if err := <-done; err == nil {
		t.Fatal("expected relay timeout error")
	}
}

type blockingHTTPDialer struct{}

func (blockingHTTPDialer) DialContext(ctx context.Context, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestConnectPreservesBufferedTunnelData(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- (&Server{Dialer: echoDialer{}}).ServeConn(context.Background(), server) }()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	request := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\nearly-data"
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "HTTP/1.1 200 Connection Established\r\n\r\n" {
		t.Fatalf("response %q", response)
	}
	data := make([]byte, len("early-data"))
	if _, err := io.ReadFull(client, data); err != nil {
		t.Fatal(err)
	}
	if string(data) != "early-data" {
		t.Fatalf("data %q", data)
	}
	_ = client.Close()
	<-done
}

func TestForwardsAbsoluteHTTPURL(t *testing.T) {
	bodyReady := make(chan struct{})
	dialer := &recordingHTTPDialer{target: make(chan string, 1), request: make(chan *http.Request, 1), bodyReady: bodyReady}
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- (&Server{Dialer: dialer}).ServeConn(context.Background(), server) }()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	request := "POST http://example.com:8080/path?q=1 HTTP/1.1\r\nHost: example.com:8080\r\nContent-Length: 3\r\nProxy-Connection: keep-alive\r\n\r\nabc"
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	if got := <-dialer.target; got != "example.com:8080" {
		t.Fatalf("dial target %q", got)
	}
	forwarded := <-dialer.request
	if forwarded.Method != http.MethodPost || forwarded.URL.Path != "/path" || forwarded.URL.RawQuery != "q=1" {
		t.Fatalf("forwarded request %#v", forwarded)
	}
	if forwarded.RequestURI != "/path?q=1" || forwarded.URL.Scheme != "" || forwarded.URL.Host != "" || forwarded.Header.Get("Proxy-Connection") != "" || forwarded.Header.Get("Proxy-Authorization") != "" || forwarded.Header.Get("Connection") != "close" {
		t.Fatalf("proxy headers were not normalized: %#v", forwarded.Header)
	}
	body, err := io.ReadAll(forwarded.Body)
	if err != nil || string(body) != "abc" {
		t.Fatalf("forwarded body %q, err=%v", body, err)
	}
	close(bodyReady)
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok" {
		t.Fatalf("response %q", response)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

type recordingHTTPDialer struct {
	target    chan string
	request   chan *http.Request
	bodyReady <-chan struct{}
}

func (d *recordingHTTPDialer) DialContext(_ context.Context, target string) (net.Conn, error) {
	d.target <- target
	local, remote := net.Pipe()
	go func() {
		request, err := http.ReadRequest(bufio.NewReader(remote))
		if err == nil {
			d.request <- request
			<-d.bodyReady
			_, _ = fmt.Fprint(remote, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		}
		_ = remote.Close()
	}()
	return local, nil
}

func TestRejectsRelativeHTTPRequest(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- (&Server{Dialer: echoDialer{}}).ServeConn(context.Background(), server) }()
	_, _ = io.WriteString(client, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	response := make([]byte, 32)
	count, err := client.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(response[:count]), "HTTP/1.1 400") {
		t.Fatalf("response %q", response[:count])
	}
	_ = client.Close()
	<-done
}

func TestRejectsHTTPSAbsoluteRequest(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- (&Server{Dialer: echoDialer{}}).ServeConn(context.Background(), server) }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(client, "GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	response := make([]byte, 64)
	count, err := client.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(response[:count]), "HTTP/1.1 400") {
		t.Fatalf("response %q", response[:count])
	}
	_ = client.Close()
	if err := <-done; err == nil {
		t.Fatal("expected HTTPS absolute request rejection")
	}
}
