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
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveCommitAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	var mu sync.Mutex
	reservation := &fakeReservation{}
	server := &Server{Path: path, ProcessIdentity: func(int) (string, error) { return "start", nil }, Reserve: func(_ context.Context, windows, wsl string) (Reservation, error) {
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
	committed, closed := reservation.values()
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

func TestReserveUDP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	reservation := &fakeReservation{}
	server := &Server{
		Path:            path,
		ProcessIdentity: func(int) (string, error) { return "start", nil },
		ReserveDatagram: func(_ context.Context, windows, wsl string) (Reservation, error) {
			if windows != "127.0.0.1:5353" || wsl != "127.0.0.1:5353" {
				t.Fatalf("UDP mapping %s -> %s", windows, wsl)
			}
			return reservation, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, path)
	response := request(t, path, "RESERVE 123 udp4 5353\n")
	if !strings.HasPrefix(response, "OK ") {
		t.Fatalf("UDP reserve: %q", response)
	}
	id := strings.TrimSpace(strings.TrimPrefix(response, "OK "))
	if got := request(t, path, "CLOSE "+id+"\n"); got != "OK\n" {
		t.Fatalf("UDP close: %q", got)
	}
	_, closed := reservation.values()
	if !closed {
		t.Fatal("UDP reservation was not closed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReapsLeaseWhenProcessIdentityDisappears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	reservation := &fakeReservation{}
	var alive atomic.Bool
	alive.Store(true)
	server := &Server{
		Path:         path,
		ReapInterval: time.Millisecond,
		ProcessIdentity: func(int) (string, error) {
			if !alive.Load() {
				return "", os.ErrNotExist
			}
			return "start", nil
		},
		Reserve: func(context.Context, string, string) (Reservation, error) { return reservation, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, path)
	if response := request(t, path, "RESERVE 456 tcp4 8001\n"); !strings.HasPrefix(response, "OK ") {
		t.Fatalf("reserve: %q", response)
	}
	alive.Store(false)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, closed := reservation.values()
		if closed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	_, closed := reservation.values()
	if !closed {
		t.Fatal("orphaned lease was not reaped")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAdoptAndReleaseKeepsLeaseUntilLastOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	reservation := &fakeReservation{}
	server := &Server{
		Path:            path,
		ProcessIdentity: func(int) (string, error) { return "start", nil },
		Reserve:         func(context.Context, string, string) (Reservation, error) { return reservation, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, path)
	response := request(t, path, "RESERVE 123 tcp4 8001\n")
	id := strings.TrimSpace(strings.TrimPrefix(response, "OK "))
	if got := request(t, path, "ADOPT 456 "+id+"\n"); got != "OK\n" {
		t.Fatalf("adopt: %q", got)
	}
	if got := request(t, path, "RELEASE 123 "+id+"\n"); got != "OK\n" {
		t.Fatalf("release parent: %q", got)
	}
	_, closed := reservation.values()
	if closed {
		t.Fatal("lease closed while child owner remained")
	}
	if got := request(t, path, "RELEASE 456 "+id+"\n"); got != "OK\n" {
		t.Fatalf("release child: %q", got)
	}
	_, closed = reservation.values()
	if !closed {
		t.Fatal("lease remained open after final owner release")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReaperKeepsLeaseWhileAnotherOwnerLives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	reservation := &fakeReservation{}
	var aliveMu sync.Mutex
	alive := map[int]bool{123: true, 456: true}
	server := &Server{
		Path: path,
		ProcessIdentity: func(pid int) (string, error) {
			aliveMu.Lock()
			defer aliveMu.Unlock()
			if !alive[pid] {
				return "", os.ErrNotExist
			}
			return "start", nil
		},
		Reserve: func(context.Context, string, string) (Reservation, error) { return reservation, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, path)
	response := request(t, path, "RESERVE 123 tcp4 8002\n")
	id := strings.TrimSpace(strings.TrimPrefix(response, "OK "))
	if got := request(t, path, "ADOPT 456 "+id+"\n"); got != "OK\n" {
		t.Fatalf("adopt: %q", got)
	}
	aliveMu.Lock()
	alive[123] = false
	aliveMu.Unlock()
	server.reapDeadProcesses()
	_, closed := reservation.values()
	if closed {
		t.Fatal("reaper closed lease while child owner remained")
	}
	aliveMu.Lock()
	alive[456] = false
	aliveMu.Unlock()
	server.reapDeadProcesses()
	_, closed = reservation.values()
	if !closed {
		t.Fatal("reaper did not close lease after final owner disappeared")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
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
	mu        sync.Mutex
	committed bool
	closed    bool
}

func (f *fakeReservation) Commit() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = true
	return nil
}
func (f *fakeReservation) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
func (f *fakeReservation) values() (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.committed, f.closed
}

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
