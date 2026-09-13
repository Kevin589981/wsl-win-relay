//go:build linux

package main

import (
	"os"
	"testing"
)

func TestPublisherAliveFindsCurrentProcess(t *testing.T) {
	alive, err := publisherAlive(os.Getpid())
	if err != nil || !alive {
		t.Fatalf("alive=%v err=%v", alive, err)
	}
}
