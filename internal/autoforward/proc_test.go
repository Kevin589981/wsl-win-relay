package autoforward

import (
	"strings"
	"testing"
)

func TestParseProcNetFindsListeningPorts(t *testing.T) {
	fixture := `  sl  local_address rem_address   st tx_queue rx_queue
   0: 0100007F:1F40 00000000:0000 0A 00000000:00000000
   1: 0100007F:1F41 0100007F:C001 01 00000000:00000000
`
	got, err := parseProcNet(strings.NewReader(fixture), "tcp4")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Port != 8000 || got[0].Network != "tcp4" {
		t.Fatalf("got %#v", got)
	}
}

func TestNormalizeListenersPrefersIPv4(t *testing.T) {
	got := normalizeListeners([]Listener{{Network: "tcp6", Port: 8000}, {Network: "tcp4", Port: 8000}, {Network: "tcp6", Port: 9000}})
	if len(got) != 2 || got[0].Network != "tcp4" || got[1].Port != 9000 {
		t.Fatalf("got %#v", got)
	}
}
