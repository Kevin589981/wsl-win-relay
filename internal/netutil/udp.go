package netutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ResolveUDPAddr is the context-aware counterpart of net.ResolveUDPAddr for
// the numeric-port endpoint format used by the relay protocol.
func ResolveUDPAddr(ctx context.Context, network, address string) (*net.UDPAddr, error) {
	return resolveUDPAddr(ctx, net.DefaultResolver, network, address)
}

func resolveUDPAddr(ctx context.Context, resolver *net.Resolver, network, address string) (*net.UDPAddr, error) {
	switch network {
	case "udp", "udp4", "udp6":
	default:
		return nil, net.UnknownNetworkError(network)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid UDP port %q", portText)
	}
	if host == "" {
		return &net.UDPAddr{Port: port}, nil
	}
	ipHost, zone := host, ""
	if index := strings.LastIndexByte(host, '%'); index >= 0 {
		ipHost, zone = host[:index], host[index+1:]
	}
	if ip := net.ParseIP(ipHost); ip != nil {
		if network == "udp4" && ip.To4() == nil {
			return nil, errors.New("non-IPv4 address for udp4")
		}
		if network == "udp6" && ip.To4() != nil {
			return nil, errors.New("non-IPv6 address for udp6")
		}
		return &net.UDPAddr{IP: ip, Port: port, Zone: zone}, nil
	}
	if zone != "" {
		return nil, fmt.Errorf("zone is only valid with an IP literal: %q", host)
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	for _, candidate := range addresses {
		if network == "udp4" && candidate.IP.To4() == nil {
			continue
		}
		if network == "udp6" && candidate.IP.To4() != nil {
			continue
		}
		return &net.UDPAddr{IP: candidate.IP, Port: port, Zone: candidate.Zone}, nil
	}
	return nil, fmt.Errorf("no %s address found for %q", network, host)
}
