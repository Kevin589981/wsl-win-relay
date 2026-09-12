package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("AABBcc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "aabbcc" {
		t.Fatalf("token=%q", got)
	}
}

func TestParseOptionsUsesTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("aabbcc"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseOptions([]string{"-endpoint", "broker", "-token-hex", "deadbeef", "-token-file", path})
	if err != nil {
		t.Fatal(err)
	}
	if opts.tokenHex != "aabbcc" || opts.tokenFile != path {
		t.Fatalf("options=%#v", opts)
	}
}

func TestLoadTokenFileRejectsInsecureOrInvalidContent(t *testing.T) {
	if runtime.GOOS != "windows" {
		path := filepath.Join(t.TempDir(), "insecure")
		if err := os.WriteFile(path, []byte("aabbcc"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTokenFile(path); err == nil {
			t.Fatal("insecure token file was accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "invalid")
	if err := os.WriteFile(path, []byte("not-hex"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTokenFile(path); err == nil {
		t.Fatal("invalid token file was accepted")
	}
}
