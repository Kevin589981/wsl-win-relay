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
)

type Reservation interface {
	Commit() error
	Close() error
}
type ReserveFunc func(context.Context, string, string) (Reservation, error)

type Server struct {
	Path        string
	WindowsHost string
	Reserve     ReserveFunc
	mu          sync.Mutex
	leases      map[uint64]*lease
	next        atomic.Uint64
}

type lease struct {
	reservation Reservation
	pid         int
	committed   bool
}

func (s *Server) Serve(ctx context.Context) error {
	if s.Path == "" || s.Reserve == nil {
		return errors.New("control socket path and reserve function are required")
	}
	if s.WindowsHost == "" {
		s.WindowsHost = "127.0.0.1"
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
	case "ABORT", "CLOSE":
		s.handleClose(conn, parts)
	default:
		writeError(conn, 22, "unknown operation")
	}
}

func (s *Server) handleReserve(ctx context.Context, conn net.Conn, parts []string) {
	if len(parts) != 4 {
		writeError(conn, 22, "RESERVE requires pid, network, and port")
		return
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil || pid <= 0 {
		writeError(conn, 22, "invalid pid")
		return
	}
	if parts[2] != "tcp4" && parts[2] != "tcp6" {
		writeError(conn, 97, "unsupported network")
		return
	}
	port, err := strconv.ParseUint(parts[3], 10, 16)
	if err != nil || port == 0 {
		writeError(conn, 22, "invalid port")
		return
	}
	windowsAddr := net.JoinHostPort(s.WindowsHost, strconv.Itoa(int(port)))
	wslHost := "127.0.0.1"
	if parts[2] == "tcp6" {
		wslHost = "::1"
	}
	wslTarget := net.JoinHostPort(wslHost, strconv.Itoa(int(port)))
	reservation, err := s.Reserve(ctx, windowsAddr, wslTarget)
	if err != nil {
		writeError(conn, errnoFor(err), err.Error())
		return
	}
	id := s.next.Add(1)
	s.mu.Lock()
	s.ensureLeases()
	s.leases[id] = &lease{reservation: reservation, pid: pid}
	s.mu.Unlock()
	_, _ = fmt.Fprintf(conn, "OK %d\n", id)
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

func (s *Server) closeAll() {
	s.mu.Lock()
	leases := s.leases
	s.leases = make(map[uint64]*lease)
	s.mu.Unlock()
	for _, l := range leases {
		_ = l.reservation.Close()
	}
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
