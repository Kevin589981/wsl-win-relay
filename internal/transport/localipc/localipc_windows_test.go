//go:build windows

package localipc

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestNamedPipeRoundTrip(t *testing.T) {
	name := "wsl-win-relay-test-" + time.Now().Format("20060102150405.000000000")
	listener, err := Listen(name)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_, err = conn.Write([]byte("ok"))
		serverDone <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got := make([]byte, 2)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "ok" {
		t.Fatalf("read=%q err=%v", got, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestNormalizePipeRejectsPathSeparator(t *testing.T) {
	if _, err := normalize(`nested\pipe`); err == nil {
		t.Fatal("path separator was accepted in a named pipe")
	}
}
