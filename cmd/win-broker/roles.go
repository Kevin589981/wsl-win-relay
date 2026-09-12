package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/broker"
	"github.com/Kevin589981/wsl-win-relay/internal/relay"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/framed"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/localipc"
	"github.com/Kevin589981/wsl-win-relay/internal/upstream"
)

const workerStartupTimeout = 10 * time.Second

const (
	roleWorker       = "worker"
	roleSocketHost   = "socket-host-v2"
	roleSocketBridge = "socket-bridge"
	roleSocketOwner  = "socket-owner"
)

var errRoleTokenMismatch = errors.New("broker role token mismatch")

func deriveEndpoint(endpoint, suffix string) string {
	if strings.HasPrefix(endpoint, `\\.\pipe\`) {
		return endpoint + "-" + suffix
	}
	return endpoint + "." + suffix
}

func runFrontend(opts options, logger *log.Logger) error {
	listener, err := localipc.Listen(opts.endpoint)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.endpoint, err)
	}
	defer listener.Close()
	service, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-service.Done()
		_ = listener.Close()
	}()
	worker := newWorkerSupervisor(opts, deriveEndpoint(opts.endpoint, "worker"), logger)
	err = worker.ensure(service)
	if err != nil {
		return err
	}
	defer worker.stop()
	logger.Printf("broker frontend listening on %s (worker %s)", opts.endpoint, worker.endpoint)
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if service.Err() != nil {
				return service.Err()
			}
			if networkErr, ok := acceptErr.(net.Error); ok && networkErr.Temporary() {
				continue
			}
			return acceptErr
		}
		go worker.bridge(service, conn)
	}
}

type workerSupervisor struct {
	ensureMu sync.Mutex
	mu       sync.Mutex
	opts     options
	endpoint string
	logger   *log.Logger
	cmd      *exec.Cmd
	done     chan struct{}
}

func newWorkerSupervisor(opts options, endpoint string, logger *log.Logger) *workerSupervisor {
	return &workerSupervisor{opts: opts, endpoint: endpoint, logger: logger}
}

func (s *workerSupervisor) ensure(ctx context.Context) error {
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()

	probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err := probeRole(probeCtx, s.endpoint, s.opts.tokenHex, roleWorker)
	cancel()
	if err == nil {
		return nil
	}
	s.stopOwnedWorker()
	cmd, err := ensureWorker(ctx, s.opts, s.endpoint, s.logger)
	if err != nil {
		return err
	}
	if cmd != nil {
		done := make(chan struct{})
		s.mu.Lock()
		s.cmd = cmd
		s.done = done
		s.mu.Unlock()
		go s.watchWorker(cmd, done)
	}
	return nil
}

func (s *workerSupervisor) stopOwnedWorker() {
	s.mu.Lock()
	cmd, done := s.cmd, s.done
	s.cmd = nil
	s.done = nil
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	if done == nil {
		_, _ = cmd.Process.Wait()
		return
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		s.logger.Printf("timed out waiting for broker worker to exit")
	}
}

func (s *workerSupervisor) watchWorker(cmd *exec.Cmd, done chan struct{}) {
	if _, err := cmd.Process.Wait(); err != nil {
		s.logger.Printf("broker worker exited: %v", err)
	}
	s.mu.Lock()
	if s.cmd == cmd {
		s.cmd = nil
		s.done = nil
	}
	s.mu.Unlock()
	close(done)
}

func (s *workerSupervisor) bridge(ctx context.Context, client net.Conn) {
	defer client.Close()
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	worker, err := localipc.Dial(dialCtx, s.endpoint)
	cancel()
	if err != nil {
		s.logger.Printf("connect broker worker failed: %v; ensuring worker", err)
		if err := s.ensure(ctx); err != nil {
			s.logger.Printf("connect broker worker: %v", err)
			return
		}
		dialCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		worker, err = localipc.Dial(dialCtx, s.endpoint)
		cancel()
		if err != nil {
			s.logger.Printf("connect broker worker after restart: %v", err)
			return
		}
	}
	defer worker.Close()
	bridgeConnections(client, worker)
}

func (s *workerSupervisor) stop() {
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	requestWorkerStop(s.endpoint)
	s.stopOwnedWorker()
}

func ensureWorker(ctx context.Context, opts options, endpoint string, logger *log.Logger) (*exec.Cmd, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err := probeRole(probeCtx, endpoint, opts.tokenHex, roleWorker)
	cancel()
	if err == nil {
		logger.Printf("reusing broker worker on %s", endpoint)
		return nil, nil
	}
	logger.Printf("broker worker probe failed on %s: %v", endpoint, err)
	if errors.Is(err, errRoleTokenMismatch) {
		if stopErr := stopConflictingRole(ctx, endpoint, logger); stopErr != nil {
			return nil, stopErr
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate broker executable: %w", err)
	}
	args := []string{"-worker", "-endpoint", endpoint, "-token-hex", opts.tokenHex}
	if opts.upstreamProxy != "" {
		args = append(args, "-upstream-proxy", opts.upstreamProxy)
	}
	worker := exec.Command(executable, args...)
	worker.Stdout = io.Discard
	worker.Stderr = os.Stderr
	logger.Printf("starting broker worker on %s", endpoint)
	if err := worker.Start(); err != nil {
		return nil, fmt.Errorf("start broker worker: %w", err)
	}
	deadline := time.NewTimer(workerStartupTimeout)
	defer deadline.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		dialErr := probeRole(probeCtx, endpoint, opts.tokenHex, roleWorker)
		cancel()
		if dialErr == nil {
			return worker, nil
		}
		select {
		case <-ctx.Done():
			_ = worker.Process.Kill()
			_, _ = worker.Process.Wait()
			return nil, ctx.Err()
		case <-deadline.C:
			_ = worker.Process.Kill()
			_, _ = worker.Process.Wait()
			return nil, fmt.Errorf("broker worker did not become ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func bridgeConnections(a, b net.Conn) {
	errCh := make(chan error, 2)
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
			_ = src.Close()
		}
		errCh <- err
	}
	go copyOne(a, b)
	go copyOne(b, a)
	<-errCh
	<-errCh
}

func runWorker(opts options, logger *log.Logger) error {
	service, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ownerEndpoint := deriveEndpoint(opts.endpoint, "owner")
	hostEndpoint := deriveEndpoint(opts.endpoint, "host")
	ownerCmd, ownerDone, err := ensureSocketOwner(service, opts, ownerEndpoint, logger)
	if err != nil {
		return err
	}
	hostCmd, hostDone, err := ensureSocketHost(service, opts, hostEndpoint, ownerEndpoint, logger)
	if err != nil {
		if ownerCmd != nil {
			requestWorkerStop(ownerEndpoint)
			_ = ownerCmd.Process.Kill()
			select {
			case <-ownerDone:
			case <-time.After(time.Second):
			}
		}
		return err
	}
	if hostCmd != nil {
		go func() {
			select {
			case <-hostDone:
				stop()
			case <-service.Done():
			}
		}()
	}
	if ownerCmd != nil {
		go func() {
			select {
			case <-ownerDone:
				stop()
			case <-service.Done():
			}
		}()
	}
	defer func() {
		if hostCmd != nil {
			requestWorkerStop(hostEndpoint)
			_ = hostCmd.Process.Kill()
			select {
			case <-hostDone:
			case <-time.After(time.Second):
			}
		}
		if ownerCmd != nil {
			requestWorkerStop(ownerEndpoint)
			_ = ownerCmd.Process.Kill()
			select {
			case <-ownerDone:
			case <-time.After(time.Second):
			}
		}
	}()
	go monitorSocketHost(service, hostEndpoint, opts.tokenHex, roleSocketHost, stop)
	listener, err := localipc.Listen(opts.endpoint)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.endpoint, err)
	}
	defer listener.Close()
	controlListener, err := localipc.Listen(deriveEndpoint(opts.endpoint, "control"))
	if err != nil {
		return fmt.Errorf("listen worker control: %w", err)
	}
	defer controlListener.Close()
	go func() {
		<-service.Done()
		_ = listener.Close()
		_ = controlListener.Close()
	}()
	go serveWorkerControl(service, controlListener, stop, opts.tokenHex, roleWorker)
	logger.Printf("broker bridge worker listening on %s (socket host bridge %s, socket owner %s)", opts.endpoint, hostEndpoint, ownerEndpoint)
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if service.Err() != nil {
				return service.Err()
			}
			if networkErr, ok := acceptErr.(net.Error); ok && networkErr.Temporary() {
				continue
			}
			return acceptErr
		}
		go bridgeWorkerConnection(service, conn, hostEndpoint, stop)
	}
}

func monitorSocketHost(ctx context.Context, endpoint, tokenHex, role string, stop context.CancelFunc) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			err := probeRole(probeCtx, endpoint, tokenHex, role)
			cancel()
			if err != nil {
				stop()
				return
			}
		}
	}
}

func bridgeWorkerConnection(ctx context.Context, client net.Conn, endpoint string, stop context.CancelFunc) {
	defer client.Close()
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	host, err := localipc.Dial(dialCtx, endpoint)
	cancel()
	if err != nil {
		stop()
		return
	}
	defer host.Close()
	bridgeConnections(client, host)
}

func ensureSocketOwner(ctx context.Context, opts options, endpoint string, logger *log.Logger) (*exec.Cmd, <-chan struct{}, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err := probeRole(probeCtx, endpoint, opts.tokenHex, roleSocketOwner)
	cancel()
	if err == nil {
		logger.Printf("reusing broker socket owner on %s", endpoint)
		return nil, nil, nil
	}
	logger.Printf("socket owner probe failed on %s: %v", endpoint, err)
	if errors.Is(err, errRoleTokenMismatch) {
		if stopErr := stopConflictingRole(ctx, endpoint, logger); stopErr != nil {
			return nil, nil, stopErr
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("locate broker executable: %w", err)
	}
	args := []string{"-socket-owner", "-endpoint", endpoint, "-token-hex", opts.tokenHex}
	if opts.upstreamProxy != "" {
		args = append(args, "-upstream-proxy", opts.upstreamProxy)
	}
	host := exec.Command(executable, args...)
	host.Stdout = io.Discard
	host.Stderr = os.Stderr
	logger.Printf("starting broker socket owner on %s", endpoint)
	if err := host.Start(); err != nil {
		return nil, nil, fmt.Errorf("start broker socket owner: %w", err)
	}
	done := make(chan struct{})
	go func() {
		if _, waitErr := host.Process.Wait(); waitErr != nil {
			logger.Printf("broker socket owner exited: %v", waitErr)
		}
		close(done)
	}()
	deadline := time.NewTimer(workerStartupTimeout)
	defer deadline.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		dialErr := probeRole(probeCtx, endpoint, opts.tokenHex, roleSocketOwner)
		cancel()
		if dialErr == nil {
			return host, done, nil
		}
		select {
		case <-ctx.Done():
			_ = host.Process.Kill()
			return nil, nil, ctx.Err()
		case <-done:
			return nil, nil, errors.New("broker socket owner exited before becoming ready")
		case <-deadline.C:
			_ = host.Process.Kill()
			return nil, nil, errors.New("broker socket owner did not become ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func ensureSocketHost(ctx context.Context, opts options, endpoint, ownerEndpoint string, logger *log.Logger) (*exec.Cmd, <-chan struct{}, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err := probeRole(probeCtx, endpoint, opts.tokenHex, roleSocketHost)
	cancel()
	if err == nil {
		logger.Printf("reusing broker socket host bridge on %s", endpoint)
		return nil, nil, nil
	}
	logger.Printf("socket host bridge probe failed on %s: %v", endpoint, err)
	if errors.Is(err, errRoleTokenMismatch) {
		if stopErr := stopConflictingRole(ctx, endpoint, logger); stopErr != nil {
			return nil, nil, stopErr
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("locate broker executable: %w", err)
	}
	args := []string{"-socket-host", "-endpoint", endpoint, "-owner-endpoint", ownerEndpoint, "-token-hex", opts.tokenHex}
	bridge := exec.Command(executable, args...)
	bridge.Stdout = io.Discard
	bridge.Stderr = os.Stderr
	logger.Printf("starting broker socket host bridge on %s (owner %s)", endpoint, ownerEndpoint)
	if err := bridge.Start(); err != nil {
		return nil, nil, fmt.Errorf("start broker socket host bridge: %w", err)
	}
	done := make(chan struct{})
	go func() {
		if _, waitErr := bridge.Process.Wait(); waitErr != nil {
			logger.Printf("broker socket host bridge exited: %v", waitErr)
		}
		close(done)
	}()
	deadline := time.NewTimer(workerStartupTimeout)
	defer deadline.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		dialErr := probeRole(probeCtx, endpoint, opts.tokenHex, roleSocketHost)
		cancel()
		if dialErr == nil {
			return bridge, done, nil
		}
		select {
		case <-ctx.Done():
			_ = bridge.Process.Kill()
			return nil, nil, ctx.Err()
		case <-done:
			return nil, nil, errors.New("broker socket host bridge exited before becoming ready")
		case <-deadline.C:
			_ = bridge.Process.Kill()
			return nil, nil, errors.New("broker socket host bridge did not become ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func runSocketOwner(opts options, logger *log.Logger) error {
	token, err := hex.DecodeString(opts.tokenHex)
	if err != nil || len(token) == 0 {
		return errors.New("attach token must be non-empty hexadecimal")
	}
	registry, err := attach.NewWithToken(token)
	if err != nil {
		return fmt.Errorf("attach registry: %w", err)
	}
	upstreamDialer, err := upstream.New(opts.upstreamProxy)
	if err != nil {
		return fmt.Errorf("upstream proxy: %w", err)
	}
	listener, err := localipc.Listen(opts.endpoint)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.endpoint, err)
	}
	defer listener.Close()
	controlListener, err := localipc.Listen(deriveEndpoint(opts.endpoint, "control"))
	if err != nil {
		return fmt.Errorf("listen worker control: %w", err)
	}
	defer controlListener.Close()
	service, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go serveWorkerControl(service, controlListener, stop, opts.tokenHex, roleSocketOwner)
	b := broker.NewWithRegistry(registry, 0)
	link := framed.New()
	server := relay.NewServerWithLink(link, upstreamDialer.DialContext, upstreamDialer.OpenPacketContext)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeAttached(service) }()
	logger.Printf("broker socket owner listening on %s", opts.endpoint)
	serveErr := b.ServeAttachedWith(service, listener, link, func(session *broker.Session) {
		server.SetPeerInstanceID(session.InstanceID())
	})
	_ = link.Close()
	serverErr := <-serverDone
	if serveErr == nil && serverErr != nil && !errors.Is(serverErr, context.Canceled) && !errors.Is(serverErr, framed.ErrClosed) {
		serveErr = serverErr
	}
	return serveErr
}

func runSocketBridge(opts options, logger *log.Logger, role string) error {
	if opts.ownerEndpoint == "" {
		return errors.New("socket bridge owner endpoint is required")
	}
	service, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := localipc.Listen(opts.endpoint)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.endpoint, err)
	}
	defer listener.Close()
	controlListener, err := localipc.Listen(deriveEndpoint(opts.endpoint, "control"))
	if err != nil {
		return fmt.Errorf("listen socket bridge control: %w", err)
	}
	defer controlListener.Close()
	go func() {
		<-service.Done()
		_ = listener.Close()
		_ = controlListener.Close()
	}()
	go serveWorkerControl(service, controlListener, stop, opts.tokenHex, role)
	go monitorSocketHost(service, opts.ownerEndpoint, opts.tokenHex, roleSocketOwner, stop)
	logger.Printf("broker socket bridge listening on %s (role %s, socket owner %s)", opts.endpoint, role, opts.ownerEndpoint)
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if service.Err() != nil {
				return service.Err()
			}
			if networkErr, ok := acceptErr.(net.Error); ok && networkErr.Temporary() {
				continue
			}
			return acceptErr
		}
		go bridgeSocketConnection(service, conn, opts.ownerEndpoint, stop)
	}
}

func bridgeSocketConnection(ctx context.Context, client net.Conn, ownerEndpoint string, stop context.CancelFunc) {
	defer client.Close()
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	owner, err := localipc.Dial(dialCtx, ownerEndpoint)
	cancel()
	if err != nil {
		stop()
		return
	}
	defer owner.Close()
	bridgeConnections(client, owner)
}

func serveWorkerControl(ctx context.Context, listener net.Listener, stop context.CancelFunc, tokenHex, role string) {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		go func() {
			defer conn.Close()
			line, _ := bufio.NewReader(io.LimitReader(conn, 128)).ReadString('\n')
			fields := strings.Fields(line)
			switch {
			case len(fields) == 3 && fields[0] == "PING" && fields[2] == role && equalTokenHex(fields[1], tokenHex):
				_, _ = io.WriteString(conn, "PONG\n")
			case len(fields) == 1 && fields[0] == "STOP":
				_, _ = io.WriteString(conn, "OK\n")
				stop()
			default:
				_, _ = io.WriteString(conn, "ERR\n")
			}
		}()
	}
}

func probeRole(ctx context.Context, endpoint, tokenHex, role string) error {
	conn, err := localipc.Dial(ctx, deriveEndpoint(endpoint, "control"))
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := io.WriteString(conn, "PING "+tokenHex+" "+role+"\n"); err != nil {
		return err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 64)).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "PONG" {
		if strings.TrimSpace(line) == "ERR" {
			return errRoleTokenMismatch
		}
		return fmt.Errorf("unexpected control probe response %q", strings.TrimSpace(line))
	}
	return nil
}

func stopConflictingRole(ctx context.Context, endpoint string, logger *log.Logger) error {
	logger.Printf("stopping broker role with mismatched token on %s", endpoint)
	requestWorkerStop(endpoint)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		err := probeRole(probeCtx, endpoint, "conflict-probe", "conflict-probe")
		cancel()
		if !errors.Is(err, errRoleTokenMismatch) && err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("broker role with mismatched token did not stop: %s", endpoint)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func equalTokenHex(provided, expected string) bool {
	providedBytes, providedErr := hex.DecodeString(provided)
	expectedBytes, expectedErr := hex.DecodeString(expected)
	if providedErr != nil || expectedErr != nil || len(providedBytes) == 0 || len(providedBytes) != len(expectedBytes) {
		return false
	}
	return subtle.ConstantTimeCompare(providedBytes, expectedBytes) == 1
}

func requestWorkerStop(endpoint string) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	conn, err := localipc.Dial(ctx, deriveEndpoint(endpoint, "control"))
	if err != nil {
		return
	}
	_, _ = io.WriteString(conn, "STOP\n")
	_ = conn.Close()
}
