package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

type Mapping struct {
	Windows string
	WSL     string
}

func ParseMapping(value string) (Mapping, error) {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return Mapping{}, fmt.Errorf("invalid reverse mapping %q; expected WINDOWS_ADDR=WSL_TARGET", value)
	}
	windows, wsl := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if err := validateEndpoint(windows); err != nil {
		return Mapping{}, fmt.Errorf("invalid Windows address %q: %w", windows, err)
	}
	if err := validateEndpoint(wsl); err != nil {
		return Mapping{}, fmt.Errorf("invalid WSL target %q: %w", wsl, err)
	}
	return Mapping{Windows: windows, WSL: wsl}, nil
}

func validateEndpoint(value string) error {
	_, rawPort, err := net.SplitHostPort(value)
	if err != nil {
		return errors.New("expected host:port")
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return errors.New("port must be an integer between 1 and 65535")
	}
	return nil
}

type Mappings []Mapping

func (m *Mappings) Set(value string) error {
	mapping, err := ParseMapping(value)
	if err != nil {
		return err
	}
	*m = append(*m, mapping)
	return nil
}

func (m *Mappings) String() string {
	parts := make([]string, 0, len(*m))
	for _, mapping := range *m {
		parts = append(parts, mapping.Windows+"="+mapping.WSL)
	}
	return strings.Join(parts, ",")
}

type Opener interface {
	ReverseForward(context.Context, string, string) (io.Closer, error)
}

type DatagramOpener interface {
	ReverseDatagramForward(context.Context, string, string) (io.Closer, error)
}

type Set struct {
	mu      sync.Mutex
	closers []io.Closer
	closed  bool
}

func OpenAll(ctx context.Context, opener Opener, mappings []Mapping) (*Set, error) {
	return openAll(ctx, mappings, func(mapping Mapping) (io.Closer, error) {
		return opener.ReverseForward(ctx, mapping.Windows, mapping.WSL)
	})
}

func OpenDatagramAll(ctx context.Context, opener DatagramOpener, mappings []Mapping) (*Set, error) {
	return openAll(ctx, mappings, func(mapping Mapping) (io.Closer, error) {
		return opener.ReverseDatagramForward(ctx, mapping.Windows, mapping.WSL)
	})
}

func openAll(ctx context.Context, mappings []Mapping, open func(Mapping) (io.Closer, error)) (*Set, error) {
	set := &Set{}
	for _, mapping := range mappings {
		closer, err := open(mapping)
		if err != nil {
			_ = set.Close()
			return nil, fmt.Errorf("%s=%s: %w", mapping.Windows, mapping.WSL, err)
		}
		if closer == nil {
			_ = set.Close()
			return nil, fmt.Errorf("%s=%s: opener returned nil mapping", mapping.Windows, mapping.WSL)
		}
		set.closers = append(set.closers, closer)
	}
	return set, nil
}

func (s *Set) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	closers := append([]io.Closer(nil), s.closers...)
	s.closers = nil
	s.mu.Unlock()
	var first error
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i].Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
