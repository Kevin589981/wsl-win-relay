//go:build !linux

package main

import "errors"

func publisherAlive(int) (bool, error) {
	return false, errors.New("publisher liveness checks require Linux")
}
