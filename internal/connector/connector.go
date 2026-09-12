// Package connector connects a short-lived stdio relay endpoint to the
// persistent broker's local IPC transport.
package connector

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/localipc"
)

var ErrInvalidConfig = errors.New("invalid connector configuration")

type Config struct {
	Endpoint     string
	Token        []byte
	Capabilities uint64
	LastEpoch    uint64
}

type Session struct {
	Conn             net.Conn
	Epoch            uint64
	InstanceID       uint64
	PeerCapabilities uint64
	Summary          attach.Summary
}

func (c Config) validate() error {
	if c.Endpoint == "" || len(c.Token) == 0 {
		return ErrInvalidConfig
	}
	return nil
}

// Connect dials the platform-local broker and completes the attach/resume
// exchange. Cancellation closes an in-progress connection even when the
// underlying platform dial or handshake has no deadline of its own.
func Connect(ctx context.Context, config Config) (*Session, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	conn, err := localipc.Dial(ctx, config.Endpoint)
	if err != nil {
		return nil, err
	}
	stopClose := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopClose:
		}
	}()
	epoch, peerCapabilities, instanceID, summary, err := attach.ClientResumeHandshakeWithInstance(conn, config.Token, config.Capabilities, config.LastEpoch)
	close(stopClose)
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return &Session{Conn: conn, Epoch: epoch, InstanceID: instanceID, PeerCapabilities: peerCapabilities, Summary: summary}, nil
}

func (s *Session) Close() error {
	if s == nil || s.Conn == nil {
		return nil
	}
	return s.Conn.Close()
}

// Bridge copies bytes in both directions until one side closes, a copy fails,
// or ctx is canceled. It intentionally treats the two streams as opaque so
// relay framing remains owned by internal/relay.
func Bridge(ctx context.Context, left, right io.ReadWriter) error {
	if left == nil || right == nil {
		return ErrInvalidConfig
	}
	errs := make(chan error, 2)
	go copyDirection(right, left, errs)
	go copyDirection(left, right, errs)
	select {
	case err := <-errs:
		if closer, ok := left.(io.Closer); ok {
			_ = closer.Close()
		}
		if closer, ok := right.(io.Closer); ok {
			_ = closer.Close()
		}
		return err
	case <-ctx.Done():
		if closer, ok := left.(io.Closer); ok {
			_ = closer.Close()
		}
		if closer, ok := right.(io.Closer); ok {
			_ = closer.Close()
		}
		return ctx.Err()
	}
}

func copyDirection(dst io.Writer, src io.Reader, errs chan<- error) {
	_, err := io.Copy(dst, src)
	if err == nil {
		err = io.EOF
	}
	errs <- err
}
