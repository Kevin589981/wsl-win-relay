package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/relay"
)

func TestSuperviseRetriesRelayExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := supervise(ctx, options{}, log.New(io.Discard, "", 0), func(context.Context, options, *log.Logger) error {
		calls++
		if calls == 1 {
			return errRelayExited
		}
		cancel()
		return context.Canceled
	}, 0)
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestSuperviseDoesNotRetryFatalErrors(t *testing.T) {
	want := errors.New("configuration failed")
	calls := 0
	err := supervise(context.Background(), options{}, log.New(io.Discard, "", 0), func(context.Context, options, *log.Logger) error {
		calls++
		return want
	}, 0)
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestSupervisePrefersContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := supervise(ctx, options{}, log.New(io.Discard, "", 0), func(context.Context, options, *log.Logger) error {
		cancel()
		return errRelayExited
	}, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestNextRestartDelayCapsExponentialBackoff(t *testing.T) {
	if got := nextRestartDelay(2*time.Second, 30*time.Second); got != 4*time.Second {
		t.Fatalf("first backoff=%s", got)
	}
	if got := nextRestartDelay(20*time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("capped backoff=%s", got)
	}
	if got := nextRestartDelay(30*time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("stable cap=%s", got)
	}
}

func TestResetRestartDelayAfterStableSession(t *testing.T) {
	if got := resetRestartDelay(30*time.Second, 2*time.Second, time.Minute, time.Minute); got != 2*time.Second {
		t.Fatalf("reset delay=%s", got)
	}
	if got := resetRestartDelay(30*time.Second, 2*time.Second, 30*time.Second, time.Minute); got != 30*time.Second {
		t.Fatalf("premature reset delay=%s", got)
	}
}

func TestHandshakeFailureClassification(t *testing.T) {
	if !errors.Is(classifyHandshakeError(io.EOF), errRelayExited) {
		t.Fatal("EOF should trigger relay restart")
	}
	if !errors.Is(classifyHandshakeError(relay.ErrClientClosed), errRelayExited) {
		t.Fatal("closed relay should trigger relay restart")
	}
	want := errors.New("protocol mismatch")
	if errors.Is(classifyHandshakeError(want), errRelayExited) {
		t.Fatal("protocol errors must remain fatal")
	}
}

func TestRelayTransportExitClassification(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrClosedPipe} {
		if !isRelayTransportExit(err) {
			t.Fatalf("%v should trigger relay restart", err)
		}
	}
	if isRelayTransportExit(errors.New("invalid protocol magic")) {
		t.Fatal("protocol errors must remain fatal")
	}
}

func TestHandshakeTransportExitClassification(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrClosedPipe, relay.ErrClientClosed} {
		if !errors.Is(classifyHandshakeError(err), errRelayExited) {
			t.Fatalf("%v should trigger relay restart", err)
		}
	}
}

func TestClassifyRelayProcessExit(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestRelayExitHelper")
	command.Env = append(os.Environ(), "WSL_WIN_RELAY_EXIT_HELPER=1", "WSL_WIN_RELAY_EXIT_CODE=2")
	if err := command.Run(); err == nil {
		t.Fatal("helper should exit non-zero")
	} else if fatal := classifyRelayProcessExit(err); fatal == nil {
		t.Fatal("non-zero relay exit should be fatal")
	}
	command = exec.Command(os.Args[0], "-test.run=TestRelayExitHelper")
	command.Env = append(os.Environ(), "WSL_WIN_RELAY_EXIT_HELPER=1", "WSL_WIN_RELAY_EXIT_CODE=0")
	if err := command.Run(); err != nil {
		t.Fatal(err)
	} else if fatal := classifyRelayProcessExit(err); fatal != nil {
		t.Fatalf("clean relay exit should remain recoverable: %v", fatal)
	}
}

func TestRelayExitHelper(t *testing.T) {
	if os.Getenv("WSL_WIN_RELAY_EXIT_HELPER") != "1" {
		return
	}
	if os.Getenv("WSL_WIN_RELAY_EXIT_CODE") == "2" {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestSessionCompletionPrefersContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(sessionCompletion(ctx, errors.New("listener closed")), context.Canceled) {
		t.Fatal("context cancellation should win over listener error")
	}
	want := errors.New("listener closed")
	if got := sessionCompletion(context.Background(), want); got != want {
		t.Fatal("non-canceled session should preserve its error")
	}
}

func TestParsePortSet(t *testing.T) {
	got, err := parsePortSet("53, 1080,8000")
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint16{53, 1080, 8000} {
		if !got[port] {
			t.Fatalf("missing port %d", port)
		}
	}
	if _, err := parsePortSet("0"); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestParseOptionsSupportsRepeatedMappings(t *testing.T) {
	opts, err := parseOptions([]string{"-relay-exe", "/mnt/c/relay.exe", "-upstream-proxy", "socks5h://127.0.0.1:7890", "-reverse", "127.0.0.1:80=127.0.0.1:8080", "-reverse", "127.0.0.1:90=127.0.0.1:9090", "-strict-listen-host", "0.0.0.0", "-strict-listen-host6", "::"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.reverse) != 2 || opts.strictListenHost != "0.0.0.0" || opts.strictListenHost6 != "::" || opts.upstreamProxy != "socks5h://127.0.0.1:7890" {
		t.Fatalf("options: %#v", opts)
	}
}

func TestParseOptionsSupportsBrokerMode(t *testing.T) {
	opts, err := parseOptions([]string{"-broker-mode", "-relay-exe", "wsl-win-connector.exe"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.brokerMode || opts.relayExe != "wsl-win-connector.exe" {
		t.Fatalf("options: %#v", opts)
	}
}

func TestParseOptionsRejectsUpstreamProxyInBrokerMode(t *testing.T) {
	if _, err := parseOptions([]string{"-broker-mode", "-upstream-proxy", "socks5h://127.0.0.1:7890"}); err == nil || !strings.Contains(err.Error(), "Windows broker") {
		t.Fatalf("expected broker upstream configuration error, got %v", err)
	}
}

func TestParseOptionsSupportsRepeatedUDPMappings(t *testing.T) {
	opts, err := parseOptions([]string{"-reverse-udp", "127.0.0.1:5353=127.0.0.1:5353", "-reverse-udp", "127.0.0.1:5354=127.0.0.1:5354"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.reverseUDP) != 2 {
		t.Fatalf("UDP options: %#v", opts.reverseUDP)
	}
}

func TestParseOptionsRequiresExplicitUDPAllowlist(t *testing.T) {
	if _, err := parseOptions([]string{"-auto-forward", "-auto-forward-udp"}); err == nil {
		t.Fatal("expected UDP automatic forwarding allowlist error")
	}
	if _, err := parseOptions([]string{"-auto-forward-udp", "-auto-forward-udp-include", "5353"}); err == nil {
		t.Fatal("expected UDP automatic forwarding dependency error")
	}
}

func TestParseOptionsSupportsAutomaticUDPConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	content := `{"auto_forward":{"enabled":true,"udp_enabled":true,"udp_include":[5353]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseOptions([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.autoForward || !opts.autoForwardUDP || !opts.autoUDPInclude[5353] {
		t.Fatalf("options: %#v", opts)
	}
}

func TestParseOptionsRejectsUnexpectedArguments(t *testing.T) {
	if _, err := parseOptions([]string{"unexpected"}); err == nil {
		t.Fatal("expected argument error")
	}
}

func TestParseOptionsRejectsNonPositiveDurationOverrides(t *testing.T) {
	for _, argument := range []string{
		"-auto-forward-interval=0",
		"-udp-associate-idle-timeout=0",
		"-relay-handshake-timeout=0",
		"-relay-dial-timeout=0",
	} {
		if _, err := parseOptions([]string{argument}); err == nil {
			t.Fatalf("argument %q should be rejected", argument)
		}
	}
}

func TestRelayArgumentsHasNoSyntheticSubcommand(t *testing.T) {
	if got := relayArguments(options{}); len(got) != 0 {
		t.Fatalf("unexpected arguments: %v", got)
	}
	got := relayArguments(options{upstreamProxy: "socks5h://127.0.0.1:7890"})
	want := []string{"-upstream-proxy", "socks5h://127.0.0.1:7890"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments %v, want %v", got, want)
	}
}

func TestConnectorEnvironmentExportsBrokerCredentialsThroughWSLENV(t *testing.T) {
	t.Setenv("WSLENV", "PATH_TRANSLATED/p:WSL_WIN_RELAY_BROKER_ENDPOINT/u:WSL_WIN_RELAY_ATTACH_TOKEN/u:WSL_WIN_RELAY_ATTACH_TOKEN")
	env := connectorEnvironment()
	var wslenv string
	for _, entry := range env {
		if len(entry) >= len("WSLENV=") && entry[:len("WSLENV=")] == "WSLENV=" {
			wslenv = entry[len("WSLENV="):]
			break
		}
	}
	if wslenv == "" {
		t.Fatal("WSLENV was not added to connector environment")
	}
	parts := strings.Split(wslenv, ":")
	for _, required := range []string{"WSL_WIN_RELAY_BROKER_ENDPOINT", "WSL_WIN_RELAY_ATTACH_TOKEN"} {
		count := 0
		for _, part := range parts {
			if part == required || strings.HasPrefix(part, required+"/") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("WSLENV entry %q count=%d in %q", required, count, wslenv)
		}
	}
	if strings.Contains(wslenv, "/u") {
		t.Fatalf("broker credentials retained an unset flag: %q", wslenv)
	}
	if !strings.Contains(wslenv, "PATH_TRANSLATED/p") {
		t.Fatalf("existing WSLENV entry was lost: %q", wslenv)
	}
}

func TestParseOptionsLoadsConfigThenAppliesCLIOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	content := `{"relay_exe":"from-config.exe","broker_mode":true,"socks5_listen":"127.0.0.1:1100","relay_handshake_timeout":"12s","relay_dial_timeout":"45s","reverse":["127.0.0.1:80=127.0.0.1:8080"],"auto_forward":{"enabled":true,"windows_host":"127.0.0.1","interval":"250ms","include":[8000],"exclude":[53]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseOptions([]string{"-config", path, "-relay-exe", "from-cli.exe", "-auto-forward=false"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.relayExe != "from-cli.exe" || opts.socksListen != "127.0.0.1:1100" || !opts.brokerMode || opts.autoForward {
		t.Fatalf("options: %#v", opts)
	}
	if opts.autoForwardInterval != 250*time.Millisecond || opts.relayHandshakeTimeout != 12*time.Second || opts.relayDialTimeout != 45*time.Second || !opts.autoInclude[8000] || !opts.autoExclude[53] || len(opts.reverse) != 1 {
		t.Fatalf("options: %#v", opts)
	}
}

func TestAddAddressPortTracksExplicitMappingPorts(t *testing.T) {
	ports := make(map[uint16]bool)
	addAddressPort(ports, "127.0.0.1:9000")
	addAddressPort(ports, "127.0.0.1:8000")
	if !ports[8000] || !ports[9000] {
		t.Fatalf("ports=%v", ports)
	}
}
