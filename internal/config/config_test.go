package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMergesDefaultsAndRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{"relay_exe":"/mnt/c/relay.exe","reverse":["127.0.0.1:80=127.0.0.1:8080"],"auto_forward":{"enabled":true,"windows_host":"127.0.0.1","interval":"250ms","include":[8000]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SOCKS5Listen != "127.0.0.1:1080" || got.RelayExecutable != "/mnt/c/relay.exe" || !got.AutoForward.Enabled {
		t.Fatalf("config: %#v", got)
	}
	if duration, _ := got.AutoForwardDuration(); duration != 250*time.Millisecond {
		t.Fatalf("duration %s", duration)
	}
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown-field error")
	}
}
