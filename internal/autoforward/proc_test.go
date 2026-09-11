package autoforward

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcScannerAllowsMissingIPv6Table(t *testing.T) {
	dir := t.TempDir()
	tcpPath := filepath.Join(dir, "tcp")
	if err := os.WriteFile(tcpPath, []byte("  sl  local_address rem_address   st tx_queue rx_queue\n   0: 0100007F:1F40 00000000:0000 0A 00000000:00000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listeners, err := (ProcScanner{TCPPath: tcpPath, TCP6Path: filepath.Join(dir, "missing-tcp6")}).Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 || listeners[0].Port != 8000 {
		t.Fatalf("listeners=%#v", listeners)
	}
}

func TestParseProcNetFindsListeningPorts(t *testing.T) {
	fixture := `  sl  local_address rem_address   st tx_queue rx_queue
   0: 0100007F:1F40 00000000:0000 0A 00000000:00000000
   1: 0100007F:1F41 0100007F:C001 01 00000000:00000000
`
	got, err := parseProcNet(strings.NewReader(fixture), "tcp4")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Port != 8000 || got[0].Network != "tcp4" || got[0].Host != "127.0.0.1" {
		t.Fatalf("got %#v", got)
	}
}

func TestNormalizeListenersPrefersIPv4(t *testing.T) {
	got := normalizeListeners([]Listener{{Network: "tcp6", Host: "::", Port: 8000}, {Network: "tcp4", Host: "0.0.0.0", Port: 8000}, {Network: "tcp6", Host: "::1", Port: 9000}})
	if len(got) != 2 || got[0].Network != "tcp4" || got[1].Port != 9000 {
		t.Fatalf("got %#v", got)
	}
}

func TestDecodeProcIPv6Address(t *testing.T) {
	host, err := decodeProcAddress("00000000000000000000000001000000", "tcp6")
	if err != nil {
		t.Fatal(err)
	}
	if host != "::1" {
		t.Fatalf("got %s", host)
	}
}
