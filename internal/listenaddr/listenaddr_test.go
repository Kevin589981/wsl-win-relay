package listenaddr

import (
	"errors"
	"net"
	"strings"
	"testing"
)

type fakeListener struct{ address net.Addr }

func (listener *fakeListener) Accept() (net.Conn, error) { return nil, errors.New("not implemented") }
func (listener *fakeListener) Close() error              { return nil }
func (listener *fakeListener) Addr() net.Addr            { return listener.address }

func fakeListen(_ string, address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	portNumber, err := net.LookupPort("tcp", port)
	if err != nil {
		return nil, err
	}
	return &fakeListener{address: &net.TCPAddr{IP: net.ParseIP(host), Port: portNumber}}, nil
}

func TestListenTCPStaticAddress(t *testing.T) {
	listener, address, err := ListenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if address != listener.Addr().String() {
		t.Fatalf("address=%q listener=%q", address, listener.Addr())
	}
}

func TestListenTCPAutoSelectsReachableAlias(t *testing.T) {
	listener, address, err := listenTCPWith(
		"auto:1081",
		func() ([]string, error) { return []string{"127.0.0.1", "10.255.255.254"}, nil },
		func(listener net.Listener) error {
			if strings.HasPrefix(listener.Addr().String(), "127.0.0.1:") {
				return errors.New("simulated mirror loopback failure")
			}
			return nil
		},
		fakeListen,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !strings.HasPrefix(address, "10.255.255.254:") {
		t.Fatalf("selected address=%q", address)
	}
}

func TestListenTCPAutoRejectsInvalidSpec(t *testing.T) {
	for _, spec := range []string{"auto", "auto:", "auto:65536", "auto:abc"} {
		if _, _, err := listenTCPWith(spec, func() ([]string, error) { return nil, nil }, func(net.Listener) error { return nil }, fakeListen); err == nil {
			t.Fatalf("accepted invalid spec %q", spec)
		}
	}
}

func TestValidateTCPListenSpec(t *testing.T) {
	if err := ValidateTCPListenSpec("127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTCPListenSpec("auto:1080"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTCPListenSpec(""); err == nil {
		t.Fatal("empty address should fail validation")
	}
}

func TestListenTCPAutoReportsCandidateFailures(t *testing.T) {
	_, _, err := listenTCPWith(
		"auto:1081",
		func() ([]string, error) { return []string{"127.0.0.1"}, nil },
		func(net.Listener) error { return errors.New("unreachable") },
		fakeListen,
	)
	if err == nil || !strings.Contains(err.Error(), "no reachable loopback address") {
		t.Fatalf("unexpected error: %v", err)
	}
}
