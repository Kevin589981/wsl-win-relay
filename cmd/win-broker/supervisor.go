package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/localipc"
)

const (
	supervisorInitialDelay = 2 * time.Second
	supervisorMaxDelay     = 30 * time.Second
	supervisorStablePeriod = time.Minute
	supervisorStopTimeout  = 5 * time.Second
	supervisorProbeTimeout = 300 * time.Millisecond
	supervisorProbePeriod  = 500 * time.Millisecond
)

// runSupervisor owns only a private election endpoint. It is deliberately a
// thin host boundary around the normal frontend, so a frontend crash cannot
// take the service entrypoint down with it. The child keeps all existing role
// reuse and socket-owner health logic.
func runSupervisor(opts options, logger *log.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate broker executable: %w", err)
	}
	args := frontendArgs(opts)
	lock, err := acquireSupervisorLock(ctx, opts.endpoint, logger)
	if err != nil {
		return err
	}
	defer lock.Close()
	go drainSupervisorLock(ctx, lock)
	delay := supervisorInitialDelay
	for {
		waitForExistingFrontend(ctx, opts.endpoint, opts.tokenHex, logger)
		started := time.Now()
		cmd := exec.Command(executable, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start broker frontend: %w", err)
		}
		wait := make(chan error, 1)
		go func() { wait <- cmd.Wait() }()
		var waitErr error
		select {
		case waitErr = <-wait:
		case <-ctx.Done():
			stopChild(cmd, wait)
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		exitCode := processExitCode(waitErr)
		if exitCode == 0 {
			return nil
		}
		if exitCode == 2 {
			return fmt.Errorf("broker frontend exited with configuration status 2")
		}
		if frontendReachable(ctx, opts.endpoint) {
			// Another supervisor won the bind race, or an older supervisor
			// survived a WSL restart. Keep this supervisor as a standby and
			// take over only after the active frontend disappears.
			logger.Printf("broker frontend is owned by another supervisor on %s; waiting for it to exit", opts.endpoint)
			waitForFrontendExit(ctx, opts.endpoint)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logger.Printf("broker frontend on %s is unavailable; taking ownership", opts.endpoint)
			delay = supervisorInitialDelay
			continue
		}
		if waitErr != nil {
			logger.Printf("broker frontend exited with status %d: %v", exitCode, waitErr)
		} else {
			logger.Printf("broker frontend exited with status %d", exitCode)
		}
		if time.Since(started) >= supervisorStablePeriod {
			delay = supervisorInitialDelay
		}
		logger.Printf("restarting broker frontend in %s", delay)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		}
		delay = nextSupervisorDelay(delay)
	}
}

// frontendReachable only probes whether the public frontend endpoint is owned
// by a live process. The frontend has no control endpoint of its own, so a
// short connect-and-close probe is used for supervisor election. The actual
// attach handshake remains token-authenticated by the normal connector path.
func frontendReachable(ctx context.Context, endpoint string) bool {
	return endpointReachable(ctx, endpoint)
}

func endpointReachable(ctx context.Context, endpoint string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, supervisorProbeTimeout)
	defer cancel()
	conn, err := localipc.Dial(probeCtx, endpoint)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func acquireSupervisorLock(ctx context.Context, endpoint string, logger *log.Logger) (net.Listener, error) {
	lockEndpoint := deriveEndpoint(endpoint, "supervisor")
	for {
		lock, err := localipc.Listen(lockEndpoint)
		if err == nil {
			logger.Printf("broker supervisor lock acquired on %s", lockEndpoint)
			return lock, nil
		}
		logger.Printf("broker supervisor lock is owned by another process on %s; waiting", lockEndpoint)
		waitForEndpointExit(ctx, lockEndpoint)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

func drainSupervisorLock(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func waitForExistingFrontend(ctx context.Context, endpoint, tokenHex string, logger *log.Logger) {
	if !frontendReachable(ctx, endpoint) {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, supervisorProbeTimeout)
	probeErr := probeRole(probeCtx, endpoint, tokenHex, roleFrontend)
	cancel()
	switch {
	case probeErr == nil:
		logger.Printf("reusing authenticated broker frontend on %s; waiting for it to exit", endpoint)
	case errors.Is(probeErr, errRoleTokenMismatch):
		logger.Printf("broker frontend token mismatch on %s; requesting conflicting instance stop", endpoint)
		requestWorkerStop(endpoint)
	default:
		// v0.1.2 and older frontends have no control endpoint. Keep a safe
		// migration path: never race their public endpoint, but clearly log
		// that only reachability (not authenticated health) was available.
		logger.Printf("reusing legacy broker frontend on %s without authenticated health endpoint; waiting for it to exit", endpoint)
	}
	waitForFrontendExit(ctx, endpoint)
	if ctx.Err() == nil {
		logger.Printf("existing broker frontend on %s is unavailable; starting replacement", endpoint)
	}
}

func waitForFrontendExit(ctx context.Context, endpoint string) {
	waitForEndpointExit(ctx, endpoint)
}

func waitForEndpointExit(ctx context.Context, endpoint string) {
	ticker := time.NewTicker(supervisorProbePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !endpointReachable(ctx, endpoint) {
				return
			}
		}
	}
}

func stopChild(cmd *exec.Cmd, wait <-chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
		<-wait
		return
	}
	timer := time.NewTimer(supervisorStopTimeout)
	defer timer.Stop()
	select {
	case <-wait:
		return
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-wait
	}
}

func frontendArgs(opts options) []string {
	args := []string{"-endpoint", opts.endpoint}
	args = append(args, tokenArgs(opts)...)
	if opts.upstreamProxy != "" {
		args = append(args, "-upstream-proxy", opts.upstreamProxy)
	}
	return args
}

func nextSupervisorDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return supervisorInitialDelay
	}
	if current >= supervisorMaxDelay || current > supervisorMaxDelay/2 {
		return supervisorMaxDelay
	}
	return current * 2
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
		return exitErr.ProcessState.ExitCode()
	}
	return 1
}
