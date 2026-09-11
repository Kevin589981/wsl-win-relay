package listencontrol

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReserveCommitAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	var mu sync.Mutex
	reservation := &fakeReservation{}
	server := &Server{Path: path, Reserve: func(_ context.Context, windows, wsl string) (Reservation, error) {
		mu.Lock()
		defer mu.Unlock()
		if windows != "127.0.0.1:8000" || wsl != "127.0.0.1:8000" {
			t.Fatalf("mapping %s -> %s", windows, wsl)
		}
		return reservation, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, path)
	response := request(t, path, "RESERVE 123 tcp4 8000\n")
	if !strings.HasPrefix(response, "OK ") {
		t.Fatalf("reserve: %q", response)
	}
	id := strings.TrimSpace(strings.TrimPrefix(response, "OK "))
	if got := request(t, path, "COMMIT "+id+"\n"); got != "OK\n" {
		t.Fatalf("commit: %q", got)
	}
	if got := request(t, path, "CLOSE "+id+"\n"); got != "OK\n" {
		t.Fatalf("close: %q", got)
	}
	mu.Lock()
	committed, closed := reservation.committed, reservation.closed
	mu.Unlock()
	if !committed || !closed {
		t.Fatalf("committed=%v closed=%v", committed, closed)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestPrepareSocketPathRefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareSocketPath(path); err == nil {
		t.Fatal("expected refusal")
	}
}

type fakeReservation struct {
	committed bool
	closed    bool
}

func (f *fakeReservation) Commit() error { f.committed = true; return nil }
func (f *fakeReservation) Close() error  { f.closed = true; return nil }

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("control socket %s did not appear", path)
}

func request(t *testing.T, path, value string) string {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, value); err != nil {
		t.Fatal(err)
	}
	response, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return response
}
