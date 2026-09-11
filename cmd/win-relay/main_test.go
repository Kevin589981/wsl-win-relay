package main

import (
	"testing"
)

func TestParseOptionsUsesEnvironmentDefault(t *testing.T) {
	t.Setenv("WSL_WIN_RELAY_UPSTREAM_PROXY", "socks5h://127.0.0.1:7890")
	opts, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.upstreamProxy != "socks5h://127.0.0.1:7890" {
		t.Fatalf("proxy %q", opts.upstreamProxy)
	}
}

func TestParseOptionsCLIOverridesEnvironment(t *testing.T) {
	t.Setenv("WSL_WIN_RELAY_UPSTREAM_PROXY", "http://127.0.0.1:8080")
	opts, err := parseOptions([]string{"-upstream-proxy", "socks5://127.0.0.1:7890"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.upstreamProxy != "socks5://127.0.0.1:7890" {
		t.Fatalf("proxy %q", opts.upstreamProxy)
	}
}

func TestParseOptionsRejectsUnexpectedArguments(t *testing.T) {
	if _, err := parseOptions([]string{"win-relay"}); err == nil {
		t.Fatal("expected argument error")
	}
}
