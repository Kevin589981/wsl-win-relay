//go:build !windows

package localipc

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestListenDialRoundTripAndRestrictsSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	probeDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			probeDone <- err
			return
		}
		probeDone <- conn.Close()
	}()
	if _, err := Listen(path); !errors.Is(err, ErrEndpointInUse) {
		t.Fatalf("second listener err=%v", err)
	}
	if err := <-probeDone; err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode=%o, want 600", info.Mode().Perm())
	}
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
	conn, err := Dial(context.Background(), path)
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

func TestListenDoesNotRemoveNonSocketPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoint")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path); err == nil {
		t.Fatal("regular file was accepted as an IPC endpoint")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("regular file was removed")
	}
}

func TestListenerCloseDoesNotRemoveReplacedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoint")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "replacement" {
		t.Fatalf("replacement content=%q", content)
	}
}

func TestDialHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Dial(ctx, filepath.Join(t.TempDir(), "missing.sock"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
