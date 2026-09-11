package httpproxy

import (
	"context"
	"io"
	"net"
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

func TestRejectsNonConnect(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- (&Server{Dialer: echoDialer{}}).ServeConn(context.Background(), server) }()
	_, _ = io.WriteString(client, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	response := make([]byte, 32)
	count, err := client.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(response[:count]), "HTTP/1.1 405") {
		t.Fatalf("response %q", response[:count])
	}
	_ = client.Close()
	<-done
}
