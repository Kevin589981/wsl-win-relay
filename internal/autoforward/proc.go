package autoforward

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

type Listener struct {
	Network string
	Host    string
	Port    uint16
}

type ProcScanner struct {
	TCPPath  string
	TCP6Path string
}

func DefaultProcScanner() ProcScanner {
	return ProcScanner{TCPPath: "/proc/net/tcp", TCP6Path: "/proc/net/tcp6"}
}

func (s ProcScanner) Scan() ([]Listener, error) {
	var listeners []Listener
	for _, source := range []struct{ path, network string }{{s.TCPPath, "tcp4"}, {s.TCP6Path, "tcp6"}} {
		file, err := os.Open(source.path)
		if err != nil {
			if source.network == "tcp6" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("open %s: %w", source.path, err)
		}
		got, parseErr := parseProcNet(file, source.network)
		_ = file.Close()
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", source.path, parseErr)
		}
		listeners = append(listeners, got...)
	}
	return normalizeListeners(listeners), nil
}

func parseProcNet(r io.Reader, network string) ([]Listener, error) {
	scanner := bufio.NewScanner(r)
	var result []Listener
	line := 0
	for scanner.Scan() {
		line++
		if line == 1 {
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		address := strings.SplitN(fields[1], ":", 2)
		if len(address) != 2 {
			return nil, fmt.Errorf("line %d: malformed local address", line)
		}
		port, err := strconv.ParseUint(address[1], 16, 16)
		if err != nil || port == 0 {
			return nil, fmt.Errorf("line %d: invalid port %q", line, address[1])
		}
		host, err := decodeProcAddress(address[0], network)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid address: %w", line, err)
		}
		result = append(result, Listener{Network: network, Host: host, Port: uint16(port)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeProcAddress(encoded, network string) (string, error) {
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if network == "tcp4" {
		if len(decoded) != net.IPv4len {
			return "", fmt.Errorf("expected 4 bytes")
		}
		for left, right := 0, len(decoded)-1; left < right; left, right = left+1, right-1 {
			decoded[left], decoded[right] = decoded[right], decoded[left]
		}
		return net.IP(decoded).String(), nil
	}
	if len(decoded) != net.IPv6len {
		return "", fmt.Errorf("expected 16 bytes")
	}
	for offset := 0; offset < len(decoded); offset += 4 {
		for left, right := offset, offset+3; left < right; left, right = left+1, right-1 {
			decoded[left], decoded[right] = decoded[right], decoded[left]
		}
	}
	return net.IP(decoded).String(), nil
}

// Prefer IPv4 when both families listen on the same port. A wildcard IPv6
// listener is commonly dual-stack and does not need a second Windows socket.
func normalizeListeners(in []Listener) []Listener {
	byPort := make(map[uint16]Listener)
	for _, listener := range in {
		current, exists := byPort[listener.Port]
		if !exists || (current.Network == "tcp6" && listener.Network == "tcp4") {
			byPort[listener.Port] = listener
		}
	}
	result := make([]Listener, 0, len(byPort))
	for _, listener := range byPort {
		result = append(result, listener)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Port < result[j].Port })
	return result
}
