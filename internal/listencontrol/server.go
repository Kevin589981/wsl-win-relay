package listencontrol

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/localipc"
)

type Reservation interface {
	Commit() error
	Close() error
}
type ReserveFunc func(context.Context, string, string) (Reservation, error)
type ReserveDatagramFunc func(context.Context, string, string) (Reservation, error)
type ProcessIdentityFunc func(int) (string, error)

var controlRequestTimeout = 15 * time.Second

type Server struct {
	Path            string
	WindowsHost     string
	WindowsHost6    string
	Reserve         ReserveFunc
	ReserveDatagram ReserveDatagramFunc
	ProcessIdentity ProcessIdentityFunc
	ReapInterval    time.Duration
	mu              sync.Mutex
	rebindMu        sync.Mutex
	leases          map[uint64]*lease
	next            atomic.Uint64
}

type lease struct {
	mu           sync.Mutex
	reservation  Reservation
	windows      string
	wsl          string
	datagram     bool
	owners       map[int]string
	ownerTargets map[int]string
	committed    bool
}

type leaseOwner struct {
	id       uint64
	pid      int
	identity string
	target   string
}

func (s *Server) Serve(ctx context.Context) error {
	if s.Path == "" || (s.Reserve == nil && s.ReserveDatagram == nil) {
		return errors.New("control socket path and at least one reserve function are required")
	}
	if s.WindowsHost == "" {
		s.WindowsHost = "127.0.0.1"
	}
	if s.WindowsHost6 == "" {
		s.WindowsHost6 = "::1"
	}
	if s.ProcessIdentity == nil {
		s.ProcessIdentity = procProcessIdentity
	}
	if s.ReapInterval <= 0 {
		s.ReapInterval = time.Second
	}
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	if err := prepareSocketPath(s.Path); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.Path)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.Path, err)
	}
	protectedListener, err := localipc.ProtectUnixPath(listener, s.Path)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("protect control socket: %w", err)
	}
	listener = protectedListener
	if err := os.Chmod(s.Path, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("secure control socket: %w", err)
	}
	var handlers sync.WaitGroup
	var connectionsMu sync.Mutex
	connections := make(map[net.Conn]struct{})
	closeConnections := func() {
		connectionsMu.Lock()
		current := make([]net.Conn, 0, len(connections))
		for conn := range connections {
			current = append(current, conn)
		}
		connectionsMu.Unlock()
		for _, conn := range current {
			_ = conn.Close()
		}
	}
	defer func() {
		_ = listener.Close()
		closeConnections()
		handlers.Wait()
		cleanupSocketPath(s.Path)
		s.closeAll()
	}()
	go func() {
		<-serveCtx.Done()
		_ = listener.Close()
		closeConnections()
	}()
	go s.reapLoop(serveCtx)
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			select {
			case <-serveCtx.Done():
				return nil
			default:
				return acceptErr
			}
		}
		connectionsMu.Lock()
		connections[conn] = struct{}{}
		handlers.Add(1)
		connectionsMu.Unlock()
		go func() {
			defer func() {
				connectionsMu.Lock()
				delete(connections, conn)
				connectionsMu.Unlock()
				handlers.Done()
			}()
			s.handle(serveCtx, conn)
		}()
	}
}

func prepareSocketPath(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket path %s", path)
	}
	probe, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = probe.Close()
		return fmt.Errorf("control socket already in use: %s", path)
	}
	if !isStaleSocketProbeError(err) {
		return fmt.Errorf("cannot determine whether control socket is in use: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func isStaleSocketProbeError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNRESET)
}

func cleanupSocketPath(path string) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return
	}
	probe, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		// A replacement server may already own this path. Never unlink it.
		_ = probe.Close()
		return
	}
	if isStaleSocketProbeError(err) {
		_ = os.Remove(path)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	requestCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		_ = conn.Close()
	}()
	if controlRequestTimeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(controlRequestTimeout))
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 4097)).ReadString('\n')
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	parts := strings.Fields(line)
	if len(parts) == 0 {
		writeError(conn, 22, "empty request")
		return
	}
	// A RESERVE can wait for a replacement relay session. Watch the request
	// connection so a disconnected interposer does not leave that wait alive.
	go func() {
		var probe [1]byte
		for {
			if _, err := conn.Read(probe[:]); err != nil {
				cancel()
				return
			}
		}
	}()
	switch parts[0] {
	case "RESERVE":
		s.handleReserve(requestCtx, conn, parts)
	case "COMMIT":
		s.handleCommit(conn, parts)
	case "ADOPT":
		s.handleAdopt(conn, parts)
	case "RELEASE":
		s.handleRelease(conn, parts)
	case "ABORT", "CLOSE":
		s.handleClose(conn, parts)
	default:
		writeError(conn, 22, "unknown operation")
	}
}

func (s *Server) handleReserve(ctx context.Context, conn net.Conn, parts []string) {
	if len(parts) != 4 && len(parts) != 5 && len(parts) != 6 {
		writeError(conn, 22, "RESERVE requires pid, network, and port")
		return
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil || pid <= 0 {
		writeError(conn, 22, "invalid pid")
		return
	}
	if parts[2] != "tcp4" && parts[2] != "tcp6" && parts[2] != "udp4" && parts[2] != "udp6" {
		writeError(conn, 97, "unsupported network")
		return
	}
	if (parts[2] == "udp4" || parts[2] == "udp6") && s.ReserveDatagram == nil {
		writeError(conn, 95, "UDP reverse forwarding is unavailable")
		return
	}
	if (parts[2] == "tcp4" || parts[2] == "tcp6") && s.Reserve == nil {
		writeError(conn, 95, "TCP reverse forwarding is unavailable")
		return
	}
	identity, err := s.ProcessIdentity(pid)
	if err != nil {
		writeError(conn, 3, "requesting process no longer exists")
		return
	}
	port, err := strconv.ParseUint(parts[3], 10, 16)
	if err != nil || port == 0 {
		writeError(conn, 22, "invalid port")
		return
	}
	windowsHost := s.WindowsHost
	wslHost := "127.0.0.1"
	if parts[2] == "tcp6" || parts[2] == "udp6" {
		windowsHost = s.WindowsHost6
		wslHost = "::1"
	}
	if len(parts) >= 5 && parts[4] != "" {
		wslHost = parts[4]
		if (parts[2] == "tcp4" || parts[2] == "udp4") && (wslHost == "0.0.0.0" || wslHost == "::") {
			wslHost = "127.0.0.1"
		}
		if (parts[2] == "tcp6" || parts[2] == "udp6") && wslHost == "::" {
			wslHost = "::1"
		}
	}
	windowsAddr := net.JoinHostPort(windowsHost, strconv.Itoa(int(port)))
	wslTarget := net.JoinHostPort(wslHost, strconv.Itoa(int(port)))
	var reserve func(context.Context, string, string) (Reservation, error)
	if parts[2] == "udp4" || parts[2] == "udp6" {
		reserve = func(reserveCtx context.Context, windows, wsl string) (Reservation, error) {
			return s.ReserveDatagram(reserveCtx, windows, wsl)
		}
	} else {
		reserve = func(reserveCtx context.Context, windows, wsl string) (Reservation, error) {
			return s.Reserve(reserveCtx, windows, wsl)
		}
	}
	reservation, err := reserve(ctx, windowsAddr, wslTarget)
	if err != nil {
		writeError(conn, errnoFor(err), err.Error())
		return
	}
	if reservation == nil {
		writeError(conn, 5, "reservation backend returned nil")
		return
	}
	id := s.next.Add(1)
	s.mu.Lock()
	s.ensureLeases()
	ownerTarget := ""
	if len(parts) == 6 {
		ownerTarget = parts[5]
	}
	s.leases[id] = &lease{reservation: reservation, windows: windowsAddr, wsl: wslTarget, datagram: parts[2] == "udp4" || parts[2] == "udp6", owners: map[int]string{pid: identity}, ownerTargets: map[int]string{pid: ownerTarget}}
	s.mu.Unlock()
	if _, err := fmt.Fprintf(conn, "OK %d\n", id); err != nil {
		// The requester may disappear after Windows has bound the port but before
		// it receives the lease id. Do not retain an unmanageable owner lease.
		s.remove(id)
	}
}

func (s *Server) handleAdopt(conn net.Conn, parts []string) {
	id, pid, target, ok := parseAdoptParts(parts)
	if !ok {
		writeError(conn, 22, "ADOPT requires pid and lease")
		return
	}
	identity, err := s.ProcessIdentity(pid)
	if err != nil {
		writeError(conn, 3, "adopting process no longer exists")
		return
	}
	s.mu.Lock()
	l := s.leases[id]
	if l == nil {
		s.mu.Unlock()
		writeError(conn, 2, "unknown lease")
		return
	}
	if l.owners == nil {
		l.owners = make(map[int]string)
	}
	if l.ownerTargets == nil {
		l.ownerTargets = make(map[int]string)
	}
	l.owners[pid] = identity
	l.ownerTargets[pid] = target
	s.mu.Unlock()
	_, _ = io.WriteString(conn, "OK\n")
}

func (s *Server) handleRelease(conn net.Conn, parts []string) {
	id, pid, ok := parseOwnerParts(parts)
	if !ok {
		writeError(conn, 22, "RELEASE requires pid and lease")
		return
	}
	identity, err := s.ProcessIdentity(pid)
	if err != nil {
		// A process can close its final descriptor while /proc is already gone;
		// the reaper will remove the owner, so make this operation idempotent.
		_, _ = io.WriteString(conn, "OK\n")
		return
	}
	if reservation := s.removeOwner(id, pid, identity); reservation != nil {
		_ = reservation.Close()
	}
	_, _ = io.WriteString(conn, "OK\n")
}

func parseOwnerParts(parts []string) (uint64, int, bool) {
	if len(parts) != 3 {
		return 0, 0, false
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil || pid <= 0 {
		return 0, 0, false
	}
	id, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || id == 0 {
		return 0, 0, false
	}
	return id, pid, true
}

func parseAdoptParts(parts []string) (uint64, int, string, bool) {
	if len(parts) != 3 && len(parts) != 4 {
		return 0, 0, "", false
	}
	id, pid, ok := parseOwnerParts(parts[:3])
	if !ok {
		return 0, 0, "", false
	}
	target := ""
	if len(parts) == 4 {
		target = parts[3]
	}
	return id, pid, target, true
}

func (s *Server) handleCommit(conn net.Conn, parts []string) {
	id, l, ok := s.lookup(parts)
	if !ok {
		writeError(conn, 2, "unknown lease")
		return
	}
	l.mu.Lock()
	reservation := l.reservation
	if reservation == nil {
		l.mu.Unlock()
		writeError(conn, 5, "lease has no reservation")
		return
	}
	err := reservation.Commit()
	if err == nil {
		l.committed = true
	}
	l.mu.Unlock()
	if err != nil {
		s.remove(id)
		writeError(conn, errnoFor(err), err.Error())
		return
	}
	_, _ = io.WriteString(conn, "OK\n")
}

func (s *Server) handleClose(conn net.Conn, parts []string) {
	id, _, ok := s.lookup(parts)
	if !ok {
		_, _ = io.WriteString(conn, "OK\n")
		return
	}
	s.remove(id)
	_, _ = io.WriteString(conn, "OK\n")
}

func (s *Server) lookup(parts []string) (uint64, *lease, bool) {
	if len(parts) != 2 {
		return 0, nil, false
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[id]
	return id, l, ok
}

func (s *Server) remove(id uint64) {
	s.mu.Lock()
	l := s.leases[id]
	delete(s.leases, id)
	s.mu.Unlock()
	if l != nil {
		l.mu.Lock()
		reservation := l.reservation
		l.mu.Unlock()
		if reservation != nil {
			_ = reservation.Close()
		}
	}
}

func (s *Server) removeOwner(id uint64, pid int, identity string) Reservation {
	s.mu.Lock()
	l := s.leases[id]
	if l == nil || l.owners[pid] != identity {
		s.mu.Unlock()
		return nil
	}
	delete(l.owners, pid)
	if l.ownerTargets != nil {
		delete(l.ownerTargets, pid)
	}
	if len(l.owners) != 0 {
		s.mu.Unlock()
		return nil
	}
	delete(s.leases, id)
	s.mu.Unlock()
	l.mu.Lock()
	reservation := l.reservation
	l.mu.Unlock()
	return reservation
}

func (s *Server) closeAll() {
	s.mu.Lock()
	leases := s.leases
	s.leases = make(map[uint64]*lease)
	s.mu.Unlock()
	for _, l := range leases {
		l.mu.Lock()
		reservation := l.reservation
		l.mu.Unlock()
		if reservation != nil {
			_ = reservation.Close()
		}
	}
}

// Rebind recreates the Windows-side reservation for every live lease. It is
// intended for a relay session replacement: the WSL process and its lease
// ownership remain valid while the old Windows transport is gone. A failed
// rebind leaves the lease in place so a later session can retry it.
//
// The returned error is an aggregate of individual failures. Successful
// leases are still installed even when another lease cannot be restored.
func (s *Server) Rebind(ctx context.Context, reserve ReserveFunc, reserveDatagram ReserveDatagramFunc) error {
	return s.rebind(ctx, reserve, reserveDatagram, false)
}

// RetryMissing recreates only leases whose Windows reservation is currently
// absent. It is safe to call periodically while a session is healthy after a
// transient rebind failure; existing reservations are left untouched.
func (s *Server) RetryMissing(ctx context.Context, reserve ReserveFunc, reserveDatagram ReserveDatagramFunc) error {
	return s.rebind(ctx, reserve, reserveDatagram, true)
}

func (s *Server) rebind(ctx context.Context, reserve ReserveFunc, reserveDatagram ReserveDatagramFunc, onlyMissing bool) error {
	if reserve == nil && reserveDatagram == nil {
		return errors.New("at least one reserve function is required")
	}
	s.rebindMu.Lock()
	defer s.rebindMu.Unlock()
	s.mu.Lock()
	entries := make([]struct {
		id uint64
		l  *lease
	}, 0, len(s.leases))
	for id, l := range s.leases {
		entries = append(entries, struct {
			id uint64
			l  *lease
		}{id: id, l: l})
	}
	s.mu.Unlock()
	var failures []string
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			failures = append(failures, fmt.Sprintf("lease %d: %v", entry.id, ctx.Err()))
			continue
		default:
		}
		entry.l.mu.Lock()
		if onlyMissing && entry.l.reservation != nil {
			entry.l.mu.Unlock()
			continue
		}
		windows, wsl, datagram := entry.l.windows, entry.l.wsl, entry.l.datagram
		old := entry.l.reservation
		entry.l.reservation = nil
		entry.l.mu.Unlock()
		// The previous relay peer may still own the Windows socket while its
		// transport is being replaced. Release that reservation before binding
		// the replacement so rebind cannot fail with a stale EADDRINUSE.
		if old != nil {
			_ = old.Close()
		}
		var replacement Reservation
		var err error
		if datagram {
			if reserveDatagram == nil {
				failures = append(failures, fmt.Sprintf("lease %d: reservation type unavailable", entry.id))
				continue
			}
			replacement, err = reserveDatagram(ctx, windows, wsl)
		} else {
			if reserve == nil {
				failures = append(failures, fmt.Sprintf("lease %d: reservation type unavailable", entry.id))
				continue
			}
			replacement, err = reserve(ctx, windows, wsl)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("lease %d: %v", entry.id, err))
			continue
		}
		if replacement == nil {
			failures = append(failures, fmt.Sprintf("lease %d: reservation backend returned nil", entry.id))
			continue
		}
		entry.l.mu.Lock()
		if entry.l.committed {
			if err := replacement.Commit(); err != nil {
				entry.l.mu.Unlock()
				_ = replacement.Close()
				failures = append(failures, fmt.Sprintf("lease %d commit: %v", entry.id, err))
				continue
			}
		}
		s.mu.Lock()
		current := s.leases[entry.id]
		s.mu.Unlock()
		if current != entry.l {
			entry.l.mu.Unlock()
			_ = replacement.Close()
			continue
		}
		entry.l.reservation = replacement
		entry.l.mu.Unlock()
	}
	if len(failures) != 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (s *Server) reapLoop(ctx context.Context) {
	ticker := time.NewTicker(s.ReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapDeadProcesses()
		}
	}
}

func (s *Server) reapDeadProcesses() {
	s.mu.Lock()
	owners := make([]leaseOwner, 0, len(s.leases))
	for id, l := range s.leases {
		for pid, identity := range l.owners {
			target := ""
			if l.ownerTargets != nil {
				target = l.ownerTargets[pid]
			}
			owners = append(owners, leaseOwner{id: id, pid: pid, identity: identity, target: target})
		}
	}
	s.mu.Unlock()
	for _, candidate := range owners {
		identity, err := s.ProcessIdentity(candidate.pid)
		if err != nil || identity != candidate.identity || (candidate.target != "" && !processHasDescriptor(candidate.pid, candidate.target)) {
			if reservation := s.removeOwner(candidate.id, candidate.pid, candidate.identity); reservation != nil {
				_ = reservation.Close()
			}
		}
	}
}

func processHasDescriptor(pid int, target string) bool {
	if target == "" {
		return true
	}
	entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
	if err != nil {
		// Permission or transient procfs failures are not proof that the
		// descriptor disappeared; identity reaping still handles dead owners.
		return true
	}
	for _, entry := range entries {
		value, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
		if err == nil && value == target {
			return true
		}
	}
	return false
}

func procProcessIdentity(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 {
		return "", errors.New("malformed proc stat")
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) <= 19 {
		return "", errors.New("incomplete proc stat")
	}
	return fields[19], nil
}

func (s *Server) ensureLeases() {
	if s.leases == nil {
		s.leases = make(map[uint64]*lease)
	}
}
func writeError(w io.Writer, errno int, message string) {
	message = strings.ReplaceAll(message, "\n", " ")
	_, _ = fmt.Fprintf(w, "ERR %d %s\n", errno, message)
}
func errnoFor(error) int { return 98 }
