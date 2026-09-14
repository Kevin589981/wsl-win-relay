//go:build !linux

package main

import (
	"os"
	"path/filepath"
)

func defaultListenStatusPath() string {
	return filepath.Join(os.TempDir(), "wsl-win-relay-listeners.json")
}
