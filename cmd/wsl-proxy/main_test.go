package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	opts, err := parseOptions([]string{"-relay-exe", "/mnt/c/relay.exe", "-reverse", "127.0.0.1:80=127.0.0.1:8080", "-reverse", "127.0.0.1:90=127.0.0.1:9090", "-strict-listen-host", "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.reverse) != 2 || opts.strictListenHost != "0.0.0.0" {
		t.Fatalf("options: %#v", opts)
	}
}

func TestParseOptionsRejectsUnexpectedArguments(t *testing.T) {
	if _, err := parseOptions([]string{"unexpected"}); err == nil {
		t.Fatal("expected argument error")
	}
}

func TestParseOptionsLoadsConfigThenAppliesCLIOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	content := `{"relay_exe":"from-config.exe","socks5_listen":"127.0.0.1:1100","reverse":["127.0.0.1:80=127.0.0.1:8080"],"auto_forward":{"enabled":true,"windows_host":"127.0.0.1","interval":"250ms","include":[8000],"exclude":[53]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseOptions([]string{"-config", path, "-relay-exe", "from-cli.exe", "-auto-forward=false"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.relayExe != "from-cli.exe" || opts.socksListen != "127.0.0.1:1100" || opts.autoForward {
		t.Fatalf("options: %#v", opts)
	}
	if opts.autoForwardInterval != 250*time.Millisecond || !opts.autoInclude[8000] || !opts.autoExclude[53] || len(opts.reverse) != 1 {
		t.Fatalf("options: %#v", opts)
	}
}
