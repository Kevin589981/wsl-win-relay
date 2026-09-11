package socks5

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

type echoDialer struct{ target chan string }

func (d *echoDialer) DialContext(context.Context, string) (net.Conn, error) {
	local, remote := net.Pipe()
	go func() { _, _ = io.Copy(remote, remote); _ = remote.Close() }()
	return local, nil
}

func TestServeConnConnectsDomainAndProxies(t *testing.T) {
	client, serverConn := net.Pipe()
	s := &Server{Dialer: &echoDialer{}}
	done := make(chan error, 1)
	go func() { done <- s.ServeConn(context.Background(), serverConn) }()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(client, method); err != nil {
		t.Fatal(err)
	}
	if method[0] != 5 || method[1] != 0 {
		t.Fatalf("method response: %v", method)
	}
	request := []byte{5, 1, 0, 3, 11}
	request = append(request, []byte("example.com")...)
	request = append(request, 0, 80)
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != replySucceeded {
		t.Fatalf("reply: %v", reply)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("got %q", buf)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestNegotiationRejectsAuthentication(t *testing.T) {
	client, serverConn := net.Pipe()
	s := &Server{Dialer: &echoDialer{}}
	done := make(chan error, 1)
	go func() { done <- s.ServeConn(context.Background(), serverConn) }()
	if _, err := client.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if response[1] != 0xff {
		t.Fatalf("got %v", response)
	}
	client.Close()
	<-done
}
