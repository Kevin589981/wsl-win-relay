package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

type listenStatus struct {
	ProcessID int    `json:"process_id"`
	UpdatedAt string `json:"updated_at"`
	SOCKS5    string `json:"socks5"`
	HTTP      string `json:"http"`
}

func listenerAddress(listener net.Listener) string {
	if listener == nil {
		return ""
	}
	return listener.Addr().String()
}

func publishListenStatus(path, socksAddress, httpAddress string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	if path == "auto" {
		path = defaultListenStatusPath()
	}
	if err := rejectStatusSymlink(path); err != nil {
		return nil, err
	}
	status := listenStatus{
		ProcessID: os.Getpid(),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		SOCKS5:    socksAddress,
		HTTP:      httpAddress,
	}
	data, err := json.Marshal(status)
	if err != nil {
		return nil, fmt.Errorf("encode listener status: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create listener status directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wsl-win-relay-listeners-*")
	if err != nil {
		return nil, fmt.Errorf("create listener status temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanupTemporary := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanupTemporary()
		return nil, fmt.Errorf("protect listener status temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		cleanupTemporary()
		return nil, fmt.Errorf("write listener status: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanupTemporary()
		return nil, fmt.Errorf("sync listener status: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("close listener status: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("publish listener status: %w", err)
	}
	return func() { removeOwnedListenStatus(path, data) }, nil
}

func rejectStatusSymlink(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("listener status path is a symlink: %s", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("listener status path is not a regular file: %s", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect listener status path: %w", err)
	}
	return nil
}

func removeOwnedListenStatus(path string, expected []byte) {
	if data, err := os.ReadFile(path); err == nil && bytes.Equal(data, expected) {
		_ = os.Remove(path)
	}
}
