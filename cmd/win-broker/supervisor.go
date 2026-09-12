package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const (
	supervisorInitialDelay = 2 * time.Second
	supervisorMaxDelay     = 30 * time.Second
	supervisorStablePeriod = time.Minute
	supervisorStopTimeout  = 5 * time.Second
)

// runSupervisor owns no IPC endpoint itself. It is deliberately a thin host
// boundary around the normal frontend, so a frontend crash cannot take the
// service entrypoint down with it. The child keeps all existing role reuse and
// socket-owner health logic.
func runSupervisor(opts options, logger *log.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate broker executable: %w", err)
	}
	args := frontendArgs(opts)
	delay := supervisorInitialDelay
	for {
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
	args := []string{"-endpoint", opts.endpoint, "-token-hex", opts.tokenHex}
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
