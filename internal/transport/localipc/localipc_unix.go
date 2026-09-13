//go:build !windows

// Package localipc provides the broker/connector's same-user local transport.
// Unix uses a filesystem Unix socket; Windows uses a named pipe in the
// platform-specific implementation.
package localipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

func Listen(name string) (net.Listener, error) {
	if name == "" {
		return nil, errors.New("local IPC endpoint must not be empty")
	}
	if info, err := os.Lstat(name); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("local IPC path exists and is not a socket: %s", name)
		}
		probe, probeErr := net.DialTimeout("unix", name, 100*time.Millisecond)
		if probeErr == nil {
			_ = probe.Close()
			return nil, ErrEndpointInUse
		}
		if !errors.Is(probeErr, syscall.ECONNREFUSED) && !errors.Is(probeErr, syscall.ENOENT) {
			return nil, fmt.Errorf("probe existing local IPC socket: %w", probeErr)
		}
		if err := os.Remove(name); err != nil {
			return nil, fmt.Errorf("remove stale local IPC socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect local IPC path: %w", err)
	}
	listener, err := net.Listen("unix", name)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(name)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("stat local IPC socket: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = listener.Close()
		if current, statErr := os.Lstat(name); statErr == nil && os.SameFile(info, current) {
			_ = os.Remove(name)
		}
		return nil, fmt.Errorf("restrict local IPC socket: %w", err)
	}
	return &listenerWithCleanup{Listener: listener, path: name, fileInfo: info}, nil
}

func Dial(ctx context.Context, name string) (net.Conn, error) {
	if name == "" {
		return nil, errors.New("local IPC endpoint must not be empty")
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", name)
}

type listenerWithCleanup struct {
	net.Listener
	path     string
	fileInfo os.FileInfo
}

func (l *listenerWithCleanup) Close() error {
	err := l.Listener.Close()
	var removeErr error
	if current, statErr := os.Lstat(l.path); statErr == nil {
		if l.fileInfo == nil || os.SameFile(l.fileInfo, current) {
			removeErr = os.Remove(l.path)
		}
	} else if !os.IsNotExist(statErr) {
		removeErr = statErr
	}
	if err != nil {
		return err
	}
	return removeErr
}
