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
	"time"
)

type Reservation interface {
	Commit() error
	Close() error
}
type ReserveFunc func(context.Context, string, string) (Reservation, error)
type ReserveDatagramFunc func(context.Context, string, string) (Reservation, error)
type ProcessIdentityFunc func(int) (string, error)

type Server struct {
	Path            string
	WindowsHost     string
	Reserve         ReserveFunc
	ReserveDatagram ReserveDatagramFunc
	ProcessIdentity ProcessIdentityFunc
	ReapInterval    time.Duration
	mu              sync.Mutex
	leases          map[uint64]*lease
	next            atomic.Uint64
}

type lease struct {
	reservation Reservation
	owners      map[int]string
	committed   bool
}

type leaseOwner struct {
	id       uint64
	pid      int
	identity string
}

func (s *Server) Serve(ctx context.Context) error {
	if s.Path == "" || (s.Reserve == nil && s.ReserveDatagram == nil) {
		return errors.New("control socket path and at least one reserve function are required")
	}
	if s.WindowsHost == "" {
		s.WindowsHost = "127.0.0.1"
	}
	if s.ProcessIdentity == nil {
		s.ProcessIdentity = procProcessIdentity
	}
	if s.ReapInterval <= 0 {
		s.ReapInterval = time.Second
	}
	if err := prepareSocketPath(s.Path); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.Path)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.Path, err)
	}
	defer func() { _ = listener.Close(); _ = os.Remove(s.Path); s.closeAll() }()
	if err := os.Chmod(s.Path, 0o600); err != nil {
		return fmt.Errorf("secure control socket: %w", err)
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	go s.reapLoop(ctx)
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return acceptErr
			}
		}
		go s.handle(ctx, conn)
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
	return os.Remove(path)
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	line, err := bufio.NewReader(io.LimitReader(conn, 4097)).ReadString('\n')
	if err != nil {
		return
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		writeError(conn, 22, "empty request")
		return
	}
	switch parts[0] {
	case "RESERVE":
		s.handleReserve(ctx, conn, parts)
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
	if len(parts) != 4 && len(parts) != 5 {
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
	windowsAddr := net.JoinHostPort(s.WindowsHost, strconv.Itoa(int(port)))
	wslHost := "127.0.0.1"
	if parts[2] == "tcp6" || parts[2] == "udp6" {
		wslHost = "::1"
	}
	if len(parts) == 5 && parts[4] != "" {
		wslHost = parts[4]
		if (parts[2] == "tcp4" || parts[2] == "udp4") && (wslHost == "0.0.0.0" || wslHost == "::") {
			wslHost = "127.0.0.1"
		}
		if (parts[2] == "tcp6" || parts[2] == "udp6") && wslHost == "::" {
			wslHost = "::1"
		}
	}
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
	id := s.next.Add(1)
	s.mu.Lock()
	s.ensureLeases()
	s.leases[id] = &lease{reservation: reservation, owners: map[int]string{pid: identity}}
	s.mu.Unlock()
	_, _ = fmt.Fprintf(conn, "OK %d\n", id)
}

func (s *Server) handleAdopt(conn net.Conn, parts []string) {
	id, pid, ok := parseOwnerParts(parts)
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
	l.owners[pid] = identity
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

func (s *Server) handleCommit(conn net.Conn, parts []string) {
	id, l, ok := s.lookup(parts)
	if !ok {
		writeError(conn, 2, "unknown lease")
		return
	}
	if err := l.reservation.Commit(); err != nil {
		s.remove(id)
		writeError(conn, errnoFor(err), err.Error())
		return
	}
	s.mu.Lock()
	if current := s.leases[id]; current != nil {
		current.committed = true
	}
	s.mu.Unlock()
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
		_ = l.reservation.Close()
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
	if len(l.owners) != 0 {
		s.mu.Unlock()
		return nil
	}
	delete(s.leases, id)
	s.mu.Unlock()
	return l.reservation
}

func (s *Server) closeAll() {
	s.mu.Lock()
	leases := s.leases
	s.leases = make(map[uint64]*lease)
	s.mu.Unlock()
	for _, l := range leases {
		_ = l.reservation.Close()
	}
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
			owners = append(owners, leaseOwner{id: id, pid: pid, identity: identity})
		}
	}
	s.mu.Unlock()
	for _, candidate := range owners {
		identity, err := s.ProcessIdentity(candidate.pid)
		if err != nil || identity != candidate.identity {
			if reservation := s.removeOwner(candidate.id, candidate.pid, candidate.identity); reservation != nil {
				_ = reservation.Close()
			}
		}
	}
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
