package main

import (
	"context"
	"io"
	"log"
	"runtime"
	"testing"
	"time"
)

func TestParseOptionsSocketOwnerRole(t *testing.T) {
	opts, err := parseOptions([]string{"-socket-owner", "-token-hex", "aabbcc"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.socketOwner || opts.socketHost || opts.socketBridge || opts.worker {
		t.Fatalf("unexpected role options: %#v", opts)
	}
}

func TestParseOptionsSocketBridgeRequiresOwner(t *testing.T) {
	for _, role := range []string{"-socket-host", "-socket-bridge"} {
		if _, err := parseOptions([]string{role, "-token-hex", "aabbcc"}); err == nil {
			t.Fatalf("%s without owner endpoint should fail", role)
		}
	}
	opts, err := parseOptions([]string{"-socket-host", "-owner-endpoint", "owner", "-token-hex", "aabbcc"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.socketHost || opts.ownerEndpoint != "owner" {
		t.Fatalf("unexpected socket-host options: %#v", opts)
	}
}

func TestParseOptionsRejectsMultipleInternalRoles(t *testing.T) {
	if _, err := parseOptions([]string{"-worker", "-socket-owner", "-token-hex", "aabbcc"}); err == nil {
		t.Fatal("multiple internal roles should fail")
	}
}

func TestParseOptionsSupervisor(t *testing.T) {
	opts, err := parseOptions([]string{"-supervise", "-token-hex", "aabbcc", "-endpoint", "broker"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.supervisor || opts.worker || opts.socketOwner || opts.socketHost || opts.socketBridge {
		t.Fatalf("unexpected supervisor options: %#v", opts)
	}
	if got := frontendArgs(options{endpoint: "broker", tokenHex: "aabbcc", upstreamProxy: "socks5h://proxy"}); len(got) != 6 || got[0] != "-endpoint" || got[2] != "-token-hex" || got[4] != "-upstream-proxy" {
		t.Fatalf("frontend args: %#v", got)
	}
}

func TestSupervisorDelay(t *testing.T) {
	if got := nextSupervisorDelay(0); got != supervisorInitialDelay {
		t.Fatalf("zero delay=%s", got)
	}
	if got := nextSupervisorDelay(2 * time.Second); got != 4*time.Second {
		t.Fatalf("first backoff=%s", got)
	}
	if got := nextSupervisorDelay(20 * time.Second); got != supervisorMaxDelay {
		t.Fatalf("capped backoff=%s", got)
	}
	if got := nextSupervisorDelay(supervisorMaxDelay); got != supervisorMaxDelay {
		t.Fatalf("maximum backoff=%s", got)
	}
}

func TestSupervisorLockExcludesSecondOwner(t *testing.T) {
	endpoint := "wsl-win-relay-supervisor-test"
	if runtime.GOOS != "windows" {
		endpoint = t.TempDir() + "/broker.sock"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := log.New(io.Discard, "", 0)
	lock, err := acquireSupervisorLock(ctx, endpoint, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer probeCancel()
	second, err := acquireSupervisorLock(probeCtx, endpoint, logger)
	if second != nil {
		_ = second.Close()
		t.Fatal("second supervisor acquired the lock")
	}
	if err == nil {
		t.Fatal("second supervisor unexpectedly returned success")
	}
}
