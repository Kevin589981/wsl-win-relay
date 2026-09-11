package main

import "testing"

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
