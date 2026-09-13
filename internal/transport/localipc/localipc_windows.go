//go:build windows

package localipc

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
)

const pipePrefix = `\\.\pipe\`

func Listen(name string) (net.Listener, error) {
	path, err := normalize(name)
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(path, &winio.PipeConfig{
		// Restrict access to the owner. The broker is intentionally not a
		// machine-wide network service.
		SecurityDescriptor: "D:P(A;;GA;;;OW)",
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
}

func Dial(ctx context.Context, name string) (net.Conn, error) {
	path, err := normalize(name)
	if err != nil {
		return nil, err
	}
	return winio.DialPipeContext(ctx, path)
}

// ProtectUnixPath is a no-op on Windows. The helper exists so WSL-only users
// of Unix listeners can share cleanup code without changing Windows builds.
func ProtectUnixPath(listener net.Listener, _ string) (net.Listener, error) {
	if listener == nil {
		return nil, errors.New("listener is required")
	}
	return listener, nil
}

func normalize(name string) (string, error) {
	if name == "" {
		return "", errors.New("local IPC endpoint must not be empty")
	}
	if strings.HasPrefix(name, pipePrefix) {
		if len(name) == len(pipePrefix) {
			return "", errors.New("local IPC pipe name must not be empty")
		}
		return name, nil
	}
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("local IPC pipe name must not contain path separators")
	}
	return pipePrefix + name, nil
}
