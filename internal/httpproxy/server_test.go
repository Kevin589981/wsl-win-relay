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
	reader := &handshakeReader{reader: bytes.NewReader(make([]byte, maxRequestHeaderBytes+1)), remaining: maxRequestHeaderBytes, bounded: true}
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
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	request := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\nearly-data"
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(client, request)
		writeDone <- err
	}()
	response := make([]byte, len("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
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
	request := "POST http://example.com:8080/path?q=1 HTTP/1.1\r\nHost: example.com:8080\r\nContent-Length: 3\r\nConnection: keep-alive, X-Trace-Hop\r\nX-Trace-Hop: remove-me\r\nKeep-Alive: timeout=5\r\nProxy-Connection: keep-alive\r\nTE: trailers\r\nUpgrade: h2c\r\n\r\nabc"
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
	if forwarded.RequestURI != "/path?q=1" || forwarded.URL.Scheme != "" || forwarded.URL.Host != "" || forwarded.Header.Get("Proxy-Connection") != "" || forwarded.Header.Get("Proxy-Authorization") != "" || forwarded.Header.Get("Keep-Alive") != "" || forwarded.Header.Get("TE") != "" || forwarded.Header.Get("Upgrade") != "" || forwarded.Header.Get("X-Trace-Hop") != "" || forwarded.Header.Get("Connection") != "close" {
		t.Fatalf("proxy headers were not normalized: %#v", forwarded.Header)
	}
	body, err := io.ReadAll(forwarded.Body)
	if err != nil || string(body) != "abc" {
		t.Fatalf("forwarded body %q, err=%v", body, err)
	}
	close(bodyReady)
	response, err := http.ReadResponse(bufio.NewReader(client), forwarded)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "ok" || response.Header.Get("Connection") != "" {
		t.Fatalf("response body %q, headers %v, err=%v", body, response.Header, err)
	}
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestForwardsMultipleHTTPRequestsOnOneClientConnection(t *testing.T) {
	dialer := &sequenceHTTPDialer{}
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- (&Server{Dialer: dialer}).ServeConn(context.Background(), server) }()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(client)
	for _, path := range []string{"/one", "/two"} {
		request := fmt.Sprintf("GET http://example.com%s HTTP/1.1\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n", path)
		if _, err := io.WriteString(client, request); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(body) != strings.TrimPrefix(path, "/") {
			t.Fatalf("response body %q, err=%v", body, err)
		}
		if response.Header.Get("Connection") != "" {
			t.Fatalf("hop-by-hop response header leaked: %v", response.Header)
		}
	}
	if got := dialer.calls(); got != 2 {
		t.Fatalf("origin dial count %d, want 2", got)
	}
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestRejectsOversizedSecondHTTPHeaders(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- (&Server{Dialer: &sequenceHTTPDialer{}}).ServeConn(context.Background(), server) }()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(client, "GET http://example.com/one HTTP/1.1\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	largeHeader := strings.Repeat("a", maxRequestHeaderBytes)
	writeDone := make(chan error, 1)
	go func() {
		_, err := fmt.Fprintf(client, "GET http://example.com/two HTTP/1.1\r\nHost: example.com\r\nX-Large: %s\r\n\r\n", largeHeader)
		writeDone <- err
	}()
	errorResponse := make([]byte, 64)
	count, err := client.Read(errorResponse)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(errorResponse[:count]), "HTTP/1.1 400") {
		t.Fatalf("response %q", errorResponse[:count])
	}
	_ = client.Close()
	<-writeDone
	if err := <-done; err == nil {
		t.Fatal("oversized second request unexpectedly succeeded")
	}
}

type sequenceHTTPDialer struct {
	count int
}

func (d *sequenceHTTPDialer) calls() int { return d.count }

func (d *sequenceHTTPDialer) DialContext(_ context.Context, _ string) (net.Conn, error) {
	d.count++
	local, remote := net.Pipe()
	go func() {
		request, err := http.ReadRequest(bufio.NewReader(remote))
		if err == nil {
			path := strings.TrimPrefix(request.URL.Path, "/")
			_, _ = fmt.Fprintf(remote, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(path), path)
		}
		_ = remote.Close()
	}()
	return local, nil
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
