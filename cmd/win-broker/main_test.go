package main

import "testing"

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
