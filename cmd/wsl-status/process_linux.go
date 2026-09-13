//go:build linux

package main

import (
	"errors"
	"syscall"
)

func publisherAlive(processID int) (bool, error) {
	err := syscall.Kill(processID, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}
