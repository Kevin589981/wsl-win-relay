package main

import (
	"bufio"
	"context"
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
	mu       sync.Mutex
	opts     options
	endpoint string
	logger   *log.Logger
	cmd      *exec.Cmd
}

func newWorkerSupervisor(opts options, endpoint string, logger *log.Logger) *workerSupervisor {
	return &workerSupervisor{opts: opts, endpoint: endpoint, logger: logger}
}

func (s *workerSupervisor) ensure(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	conn, err := localipc.Dial(probeCtx, s.endpoint)
	cancel()
	if err == nil {
		_ = conn.Close()
		return nil
	}
	if s.cmd != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
		s.cmd = nil
	}
	cmd, err := ensureWorker(ctx, s.opts, s.endpoint, s.logger)
	if err != nil {
		return err
	}
	s.cmd = cmd
	return nil
}

func (s *workerSupervisor) bridge(ctx context.Context, client net.Conn) {
	defer client.Close()
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	worker, err := localipc.Dial(dialCtx, s.endpoint)
	cancel()
	if err != nil {
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
	requestWorkerStop(s.endpoint)
	s.mu.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
	}
}

func ensureWorker(ctx context.Context, opts options, endpoint string, logger *log.Logger) (*exec.Cmd, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	conn, err := localipc.Dial(probeCtx, endpoint)
	cancel()
	if err == nil {
		_ = conn.Close()
		logger.Printf("reusing broker worker on %s", endpoint)
		return nil, nil
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
	if err := worker.Start(); err != nil {
		return nil, fmt.Errorf("start broker worker: %w", err)
	}
	deadline := time.NewTimer(workerStartupTimeout)
	defer deadline.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		conn, dialErr := localipc.Dial(probeCtx, endpoint)
		cancel()
		if dialErr == nil {
			_ = conn.Close()
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
	go serveWorkerControl(service, controlListener, stop)
	b := broker.NewWithRegistry(registry, 0)
	link := framed.New()
	server := relay.NewServerWithLink(link, upstreamDialer.DialContext, upstreamDialer.OpenPacketContext)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeAttached(service) }()
	logger.Printf("broker worker listening on %s", opts.endpoint)
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

func serveWorkerControl(ctx context.Context, listener net.Listener, stop context.CancelFunc) {
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
			line, _ := bufio.NewReader(io.LimitReader(conn, 64)).ReadString('\n')
			if strings.TrimSpace(line) == "STOP" {
				_, _ = io.WriteString(conn, "OK\n")
				stop()
			}
		}()
	}
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
