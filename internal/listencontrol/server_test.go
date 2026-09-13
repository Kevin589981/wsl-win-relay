package listencontrol

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
		if windows != "127.0.0.1:8000" || wsl != "127.0.0.2:8000" {
			t.Fatalf("mapping %s -> %s", windows, wsl)
		}
		return reservation, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, path)
	response := request(t, path, "RESERVE 123 tcp4 8000 127.0.0.2 socket:[42]\n")
	if !strings.HasPrefix(response, "OK ") {
		t.Fatalf("reserve: %q", response)
	}
	id := strings.TrimSpace(strings.TrimPrefix(response, "OK "))
	server.mu.Lock()
	lease := server.leases[1]
	gotTarget := ""
	if lease != nil {
		gotTarget = lease.ownerTargets[123]
	}
	server.mu.Unlock()
	if gotTarget != "socket:[42]" {
		t.Fatalf("stored owner target %q", gotTarget)
	}
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

func TestReserveIPv6UsesIPv6WindowsHost(t *testing.T) {
	reservation := &fakeReservation{}
	server := &Server{WindowsHost: "127.0.0.1", WindowsHost6: "::", ProcessIdentity: func(int) (string, error) { return "start", nil }, Reserve: func(_ context.Context, windows, wsl string) (Reservation, error) {
		if windows != "[::]:8000" || wsl != "[::1]:8000" {
			t.Fatalf("mapping %s -> %s", windows, wsl)
		}
		return reservation, nil
	}}
	serverSide, clientSide := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.handleReserve(context.Background(), serverSide, []string{"RESERVE", "123", "tcp6", "8000"})
		close(done)
	}()
	response := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(clientSide)
		response <- string(data)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reserve did not finish")
	}
	_ = clientSide.Close()
	if got := <-response; !strings.HasPrefix(got, "OK ") {
		t.Fatalf("reserve: %q", got)
	}
	_ = serverSide.Close()
}

func TestReserveClosesLeaseWhenRequesterDisconnects(t *testing.T) {
	reservation := &fakeReservation{}
	server := &Server{
		ProcessIdentity: func(int) (string, error) { return "start", nil },
		Reserve:         func(context.Context, string, string) (Reservation, error) { return reservation, nil },
	}
	serverSide, clientSide := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.handleReserve(context.Background(), serverSide, []string{"RESERVE", "123", "tcp4", "8000"})
		close(done)
	}()
	_ = clientSide.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reserve did not finish after requester disconnect")
	}
	_ = serverSide.Close()
	_, closed := reservation.values()
	if !closed {
		t.Fatal("lease remained open after requester disconnect")
	}
}

func TestReserveRejectsNilReservation(t *testing.T) {
	server := &Server{
		ProcessIdentity: func(int) (string, error) { return "start", nil },
		Reserve:         func(context.Context, string, string) (Reservation, error) { return nil, nil },
	}
	serverSide, clientSide := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.handleReserve(context.Background(), serverSide, []string{"RESERVE", "123", "tcp4", "8000"})
		close(done)
	}()
	response := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(clientSide)
		response <- string(data)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reserve did not finish")
	}
	_ = clientSide.Close()
	if got := <-response; got != "ERR 5 reservation backend returned nil\n" {
		t.Fatalf("response=%q", got)
	}
	_ = serverSide.Close()
}

func TestReserveCancelsWhenRequesterDisconnects(t *testing.T) {
	server := &Server{
		ProcessIdentity: func(int) (string, error) { return "start", nil },
		Reserve: func(ctx context.Context, _, _ string) (Reservation, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	serverSide, clientSide := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.handle(context.Background(), serverSide)
		close(done)
	}()
	if _, err := io.WriteString(clientSide, "RESERVE 123 tcp4 8000\n"); err != nil {
		t.Fatal(err)
	}
	_ = clientSide.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reserve handler did not cancel after requester disconnect")
	}
	_ = serverSide.Close()
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

func TestRebindReplacesCommittedReservation(t *testing.T) {
	old := &fakeReservation{}
	if err := old.Commit(); err != nil {
		t.Fatal(err)
	}
	server := &Server{leases: map[uint64]*lease{
		7: {reservation: old, windows: "127.0.0.1:8000", wsl: "127.0.0.1:8000", committed: true, owners: map[int]string{123: "start"}},
	}}
	replacement := &fakeReservation{}
	if err := server.Rebind(context.Background(), func(_ context.Context, windows, wsl string) (Reservation, error) {
		if windows != "127.0.0.1:8000" || wsl != "127.0.0.1:8000" {
			t.Fatalf("mapping %s -> %s", windows, wsl)
		}
		return replacement, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	oldCommitted, oldClosed := old.values()
	newCommitted, newClosed := replacement.values()
	if !oldCommitted || !oldClosed {
		t.Fatalf("old reservation committed=%v closed=%v", oldCommitted, oldClosed)
	}
	if got := old.closes(); got != 1 {
		t.Fatalf("old reservation close count=%d, want 1", got)
	}
	if !newCommitted || newClosed {
		t.Fatalf("replacement committed=%v closed=%v", newCommitted, newClosed)
	}
	server.mu.Lock()
	current := server.leases[7]
	server.mu.Unlock()
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.reservation != replacement || !current.committed {
		t.Fatalf("lease was not replaced: %#v", current)
	}
}

func TestRebindKeepsLeaseWhenReplacementFails(t *testing.T) {
	old := &fakeReservation{}
	server := &Server{leases: map[uint64]*lease{
		9: {reservation: old, windows: "127.0.0.1:8001", wsl: "127.0.0.1:8001", owners: map[int]string{123: "start"}},
	}}
	wantErr := errors.New("relay unavailable")
	if err := server.Rebind(context.Background(), func(context.Context, string, string) (Reservation, error) {
		return nil, wantErr
	}, nil); err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("rebind error=%v", err)
	}
	_, closed := old.values()
	if !closed {
		t.Fatal("failed rebind did not close the stale reservation")
	}
	server.mu.Lock()
	_, exists := server.leases[9]
	server.mu.Unlock()
	if !exists {
		t.Fatal("failed rebind removed the lease")
	}
}

func TestRebindUsesDatagramReservationFactory(t *testing.T) {
	old := &fakeReservation{}
	server := &Server{leases: map[uint64]*lease{
		11: {reservation: old, windows: "127.0.0.1:5353", wsl: "127.0.0.1:5353", datagram: true, owners: map[int]string{123: "start"}},
	}}
	replacement := &fakeReservation{}
	if err := server.Rebind(context.Background(), func(context.Context, string, string) (Reservation, error) {
		t.Fatal("TCP factory was used for a UDP lease")
		return nil, nil
	}, func(_ context.Context, windows, wsl string) (Reservation, error) {
		if windows != "127.0.0.1:5353" || wsl != "127.0.0.1:5353" {
			t.Fatalf("mapping %s -> %s", windows, wsl)
		}
		return replacement, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, oldClosed := old.values()
	if !oldClosed {
		t.Fatal("old UDP reservation was not closed")
	}
	server.mu.Lock()
	current := server.leases[11]
	server.mu.Unlock()
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.reservation != replacement {
		t.Fatalf("UDP lease was not replaced: %#v", current)
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

func TestReaperDropsOwnerWhenSocketDescriptorDisappears(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux procfs descriptor inspection")
	}
	reservation := &fakeReservation{}
	pid := os.Getpid()
	server := &Server{
		ProcessIdentity: func(got int) (string, error) {
			if got != pid {
				t.Fatalf("identity requested for pid %d, want %d", got, pid)
			}
			return "start", nil
		},
		leases: map[uint64]*lease{
			1: {
				reservation:  reservation,
				owners:       map[int]string{pid: "start"},
				ownerTargets: map[int]string{pid: "socket:[descriptor-that-does-not-exist]"},
			},
		},
	}

	server.reapDeadProcesses()
	_, closed := reservation.values()
	if !closed {
		t.Fatal("reaper kept lease after its socket descriptor disappeared")
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

func TestPrepareSocketPathRefusesActiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if err := prepareSocketPath(path); err == nil {
		t.Fatal("expected active socket refusal")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active socket was removed: %v", err)
	}
}

func TestPrepareSocketPathRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepareSocketPath(path); err != nil {
		t.Fatalf("prepare stale socket: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale socket remains, stat err=%v", err)
	}
}

func TestCleanupSocketPathPreservesActiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	cleanupSocketPath(path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active socket was removed: %v", err)
	}
}

func TestCleanupSocketPathRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	cleanupSocketPath(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale socket remains, stat err=%v", err)
	}
}

type fakeReservation struct {
	mu         sync.Mutex
	committed  bool
	closed     bool
	closeCount int
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
	f.closeCount++
	return nil
}
func (f *fakeReservation) values() (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.committed, f.closed
}

func (f *fakeReservation) closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCount
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
