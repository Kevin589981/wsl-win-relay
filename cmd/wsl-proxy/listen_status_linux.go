//go:build linux

package main

import (
	"fmt"
	"path/filepath"
)

func defaultListenStatusPath() string {
	return filepath.Join("/tmp", fmt.Sprintf("wsl-win-relay-%d", currentUID()), "listeners.json")
}
