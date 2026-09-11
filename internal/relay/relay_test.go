package relay

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestClientServerEcho(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, func(context.Context, string) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() { _, _ = io.Copy(remote, remote); remote.Close() }()
		return local, nil
	})
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(ctx) }()
	client := NewClient(clientSide)
	clientDone := make(chan error, 1)
	go func() { clientDone <- client.Run(ctx) }()

	conn, err := client.DialContext(ctx, "example.invalid:1234")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("got %q", buf)
	}
	conn.Close()
	client.Close()
	serverSide.Close()
	clientSide.Close()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestReverseForward(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	go func() {
		conn, acceptErr := local.Accept()
		if acceptErr == nil {
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
		}
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	windowsAddr := probe.Addr().String()
	_ = probe.Close()

	forward, err := client.ReverseForward(ctx, windowsAddr, local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()
	conn, err := net.Dial("tcp", windowsAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("reverse")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "reverse" {
		t.Fatalf("got %q", buf)
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}
