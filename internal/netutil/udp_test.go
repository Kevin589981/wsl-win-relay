package netutil

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestResolveUDPAddrPreservesLiteralSemantics(t *testing.T) {
	tests := []struct {
		network string
		value   string
		want    string
	}{
		{"udp4", "127.0.0.1:53", "127.0.0.1:53"},
		{"udp6", "[::1]:5353", "[::1]:5353"},
		{"udp6", "[fe80::1%eth0]:5353", "[fe80::1%eth0]:5353"},
		{"udp", ":0", ":0"},
	}
	for _, test := range tests {
		address, err := ResolveUDPAddr(context.Background(), test.network, test.value)
		if err != nil {
			t.Fatalf("ResolveUDPAddr(%q): %v", test.value, err)
		}
		if address.String() != test.want {
			t.Fatalf("ResolveUDPAddr(%q)=%q, want %q", test.value, address, test.want)
		}
	}
}

func TestResolveUDPAddrHonorsCancellation(t *testing.T) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveUDPAddr(ctx, resolver, "udp", "relay.invalid:53"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveUDPAddr returned %v", err)
	}
}

func TestResolveUDPAddrRejectsFamilyAndPortMismatch(t *testing.T) {
	for _, test := range []struct{ network, address string }{
		{"udp4", "[::1]:53"},
		{"udp6", "127.0.0.1:53"},
		{"udp", "127.0.0.1:http"},
		{"tcp", "127.0.0.1:53"},
	} {
		if _, err := ResolveUDPAddr(context.Background(), test.network, test.address); err == nil {
			t.Fatalf("accepted %s %s", test.network, test.address)
		}
	}
}
