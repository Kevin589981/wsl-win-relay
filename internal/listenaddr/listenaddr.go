package listenaddr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	autoPrefix   = "auto:"
	probeTimeout = 250 * time.Millisecond
)

// ValidateTCPListenSpec validates the syntax understood by ListenTCP without
// opening a socket. It is used by configuration checks.
func ValidateTCPListenSpec(spec string) error {
	if spec == "" {
		return errors.New("TCP listen address must not be empty")
	}
	if strings.HasPrefix(spec, autoPrefix) {
		port, err := autoPort(spec)
		if err == nil && port == "0" {
			return errors.New("automatic listener port must be between 1 and 65535: \"0\"")
		}
		return err
	}
	return nil
}

// ListenTCP opens a TCP listener described by spec. Ordinary host:port
// specifications retain their existing behavior. auto:<port> selects an
// address assigned to a loopback interface and verifies it with a real
// same-namespace connect before returning the listener.
func ListenTCP(spec string) (net.Listener, string, error) {
	return listenTCPWith(spec, loopbackCandidates, probeListener, net.Listen)
}

// ListenTCPOnHost behaves like ListenTCP, but tries preferredHost first for an
// automatic specification. This keeps related listeners on one selected WSL
// loopback address while retaining independent bind/connect verification.
func ListenTCPOnHost(spec, preferredHost string) (net.Listener, string, error) {
	if preferredHost == "" || !strings.HasPrefix(spec, autoPrefix) {
		return ListenTCP(spec)
	}
	return listenTCPWith(spec, func() ([]string, error) {
		addresses, err := loopbackCandidates()
		if err != nil {
			return nil, err
		}
		result := []string{preferredHost}
		for _, address := range addresses {
			if address != preferredHost {
				result = append(result, address)
			}
		}
		return result, nil
	}, probeListener, net.Listen)
}

func listenTCPWith(spec string, candidates func() ([]string, error), probe func(net.Listener) error, listen func(string, string) (net.Listener, error)) (net.Listener, string, error) {
	if !strings.HasPrefix(spec, autoPrefix) {
		listener, err := listen("tcp", spec)
		if err != nil {
			return nil, "", err
		}
		return listener, listener.Addr().String(), nil
	}
	port, err := autoPort(spec)
	if err != nil {
		return nil, "", err
	}
	addresses, err := candidates()
	if err != nil {
		return nil, "", fmt.Errorf("enumerate loopback addresses: %w", err)
	}
	var failures []string
	for _, host := range addresses {
		address := net.JoinHostPort(host, port)
		listener, listenErr := listen("tcp4", address)
		if listenErr != nil {
			failures = append(failures, fmt.Sprintf("%s: bind: %v", address, listenErr))
			continue
		}
		if probeErr := probe(listener); probeErr != nil {
			_ = listener.Close()
			failures = append(failures, fmt.Sprintf("%s: probe: %v", address, probeErr))
			continue
		}
		return listener, listener.Addr().String(), nil
	}
	if len(failures) == 0 {
		return nil, "", fmt.Errorf("no loopback addresses available for %s", spec)
	}
	return nil, "", fmt.Errorf("no reachable loopback address for %s (%s)", spec, strings.Join(failures, "; "))
}

func autoPort(spec string) (string, error) {
	port := strings.TrimPrefix(spec, autoPrefix)
	if port == "" {
		return "", errors.New("automatic listener must use auto:<port>")
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 0 || value > 65535 {
		return "", fmt.Errorf("automatic listener port must be between 0 and 65535: %q", port)
	}
	return strconv.Itoa(value), nil
}

func loopbackCandidates() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	addresses := make([]string, 0, 4)
	add := func(host string) {
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		addresses = append(addresses, host)
	}
	// Keep the conventional address first when it is healthy; auto mode will
	// reject it with the real probe if mirror-mode policy routing is broken.
	add("127.0.0.1")
	for _, iface := range interfaces {
		if iface.Name != "lo" && iface.Flags&net.FlagLoopback == 0 {
			continue
		}
		addrs, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		for _, addr := range addrs {
			ip := addressIP(addr)
			if ip == nil || ip.To4() == nil {
				continue
			}
			add(ip.To4().String())
		}
	}
	// Interface enumeration order is platform-dependent. Preserve 127.0.0.1
	// first, then sort aliases for deterministic diagnostics and tests.
	if len(addresses) > 1 {
		sort.Strings(addresses[1:])
	}
	return addresses, nil
}

func addressIP(addr net.Addr) net.IP {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		if host, _, err := net.SplitHostPort(addr.String()); err == nil {
			return net.ParseIP(host)
		}
		return net.ParseIP(addr.String())
	}
}

func probeListener(listener net.Listener) error {
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("unexpected listener address %T", listener.Addr())
	}
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		_ = conn.Close()
		accepted <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp4", tcpAddress.String())
	if err != nil {
		return err
	}
	_ = conn.Close()
	select {
	case acceptErr := <-accepted:
		return acceptErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
