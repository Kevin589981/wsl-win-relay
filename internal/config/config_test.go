package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMergesDefaultsAndRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{"relay_exe":"/mnt/c/relay.exe","upstream_proxy":"socks5h://127.0.0.1:7890","reverse":["127.0.0.1:80=127.0.0.1:8080"],"reverse_udp":["127.0.0.1:5353=127.0.0.1:5353"],"strict_listen_host6":"::1","auto_forward":{"enabled":true,"windows_host":"127.0.0.1","windows_host6":"::1","interval":"250ms","include":[8000]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SOCKS5Listen != "127.0.0.1:1080" || got.RelayExecutable != "/mnt/c/relay.exe" || got.UpstreamProxy != "socks5h://127.0.0.1:7890" || got.StrictListenHost6 != "::1" || got.AutoForward.WindowsHost6 != "::1" || len(got.ReverseUDP) != 1 || !got.AutoForward.Enabled {
		t.Fatalf("config: %#v", got)
	}
	if duration, _ := got.AutoForwardDuration(); duration != 250*time.Millisecond {
		t.Fatalf("duration %s", duration)
	}
	if duration, _ := got.UDPAssociateIdleDuration(); duration != 5*time.Minute {
		t.Fatalf("UDP idle duration %s", duration)
	}
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown-field error")
	}
}
