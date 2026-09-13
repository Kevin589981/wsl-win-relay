package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMergesDefaultsAndRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{"relay_exe":"/mnt/c/relay.exe","broker_mode":true,"upstream_proxy":"socks5h://127.0.0.1:7890","proxy_handshake_timeout":"3s","reverse":["127.0.0.1:80=127.0.0.1:8080"],"reverse_udp":["127.0.0.1:5353=127.0.0.1:5353"],"strict_listen_host6":"::1","auto_forward":{"enabled":true,"windows_host":"127.0.0.1","windows_host6":"::1","interval":"250ms","include":[8000]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SOCKS5Listen != "127.0.0.1:1080" || got.RelayExecutable != "/mnt/c/relay.exe" || !got.BrokerMode || got.UpstreamProxy != "socks5h://127.0.0.1:7890" || got.StrictListenHost6 != "::1" || got.RelayHandshakeTimeout != "5s" || got.RelayDialTimeout != "30s" || got.ProxyHandshakeTimeout != "3s" || got.AutoForward.WindowsHost6 != "::1" || len(got.ReverseUDP) != 1 || !got.AutoForward.Enabled {
		t.Fatalf("config: %#v", got)
	}
	if duration, _ := got.AutoForwardDuration(); duration != 250*time.Millisecond {
		t.Fatalf("duration %s", duration)
	}
	if minimum, maximum, err := got.AutoForwardRetryDurations(); err != nil || minimum != time.Second || maximum != 30*time.Second {
		t.Fatalf("retry durations: %s, %s, %v", minimum, maximum, err)
	}
	if duration, _ := got.UDPAssociateIdleDuration(); duration != 5*time.Minute {
		t.Fatalf("UDP idle duration %s", duration)
	}
	if duration, _ := got.RelayDialDuration(); duration != 30*time.Second {
		t.Fatalf("relay dial duration %s", duration)
	}
	if duration, _ := got.RelayHandshakeDuration(); duration != 5*time.Second {
		t.Fatalf("relay handshake duration %s", duration)
	}
	if duration, _ := got.ProxyHandshakeDuration(); duration != 3*time.Second {
		t.Fatalf("proxy handshake duration %s", duration)
	}
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestLoadNormalizesHTTPProxyListenName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, content := range []string{
		`{"http_proxy_listen":"127.0.0.1:8081"}`,
		`{"http_connect_listen":"127.0.0.1:8081"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil {
			t.Fatalf("load %s: %v", content, err)
		}
		if got.HTTPProxyListen != "127.0.0.1:8081" || got.HTTPConnectListen != "127.0.0.1:8081" {
			t.Fatalf("normalized config for %s: %#v", content, got)
		}
	}
}

func TestLoadRejectsConflictingHTTPProxyListenNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{"http_connect_listen":"127.0.0.1:8080","http_proxy_listen":"127.0.0.1:8081"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected conflicting HTTP listener names to be rejected")
	}
}

func TestAutoForwardRetryDurationsRejectInvalidValues(t *testing.T) {
	for _, values := range [][2]string{{"", "30s"}, {"0", "30s"}, {"-1s", "30s"}, {"1s", ""}, {"1s", "0"}, {"1s", "not-a-duration"}, {"30s", "1s"}} {
		file := Default()
		file.AutoForward.RetryMin, file.AutoForward.RetryMax = values[0], values[1]
		if _, _, err := file.AutoForwardRetryDurations(); err == nil {
			t.Fatalf("retry values %q/%q should be rejected", values[0], values[1])
		}
	}
}

func TestRelayHandshakeDurationRejectsNonPositiveValues(t *testing.T) {
	for _, value := range []string{"", "0", "-1s", "not-a-duration"} {
		file := Default()
		file.RelayHandshakeTimeout = value
		if _, err := file.RelayHandshakeDuration(); err == nil {
			t.Fatalf("value %q should be rejected", value)
		}
	}
}

func TestRelayDialDurationRejectsNonPositiveValues(t *testing.T) {
	for _, value := range []string{"", "0", "-1s", "not-a-duration"} {
		file := Default()
		file.RelayDialTimeout = value
		if _, err := file.RelayDialDuration(); err == nil {
			t.Fatalf("value %q should be rejected", value)
		}
	}
}

func TestProxyHandshakeDurationRejectsNonPositiveValues(t *testing.T) {
	for _, value := range []string{"", "0", "-1s", "not-a-duration"} {
		file := Default()
		file.ProxyHandshakeTimeout = value
		if _, err := file.ProxyHandshakeDuration(); err == nil {
			t.Fatalf("value %q should be rejected", value)
		}
	}
}

func TestLoadRejectsZeroAutomaticForwardPorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, field := range []string{"include", "udp_include", "exclude"} {
		content := `{"auto_forward":{"` + field + `":[0]}}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("expected zero-port rejection for %s", field)
		}
	}
}
