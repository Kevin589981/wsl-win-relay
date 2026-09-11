package main

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/Kevin589981/wsl-win-relay/internal/relay"
)

// sessionDialer keeps local proxy listeners independent from one relay child.
// Existing streams still belong to the failed session; only new operations
// wait for and use the replacement session.
type sessionDialer struct {
	mu      sync.RWMutex
	client  sessionClient
	changed chan struct{}
}

type sessionClient interface {
	DialContext(context.Context, string) (net.Conn, error)
	OpenPacketContext(context.Context) (net.PacketConn, error)
}

func newSessionDialer() *sessionDialer {
	return &sessionDialer{changed: make(chan struct{})}
}

func (d *sessionDialer) set(client sessionClient) {
	d.mu.Lock()
	previous := d.changed
	d.client = client
	d.changed = make(chan struct{})
	d.mu.Unlock()
	close(previous)
}

func (d *sessionDialer) clear(client sessionClient) {
	d.mu.Lock()
	if d.client != client {
		d.mu.Unlock()
		return
	}
	previous := d.changed
	d.client = nil
	d.changed = make(chan struct{})
	d.mu.Unlock()
	close(previous)
}

func (d *sessionDialer) DialContext(ctx context.Context, target string) (net.Conn, error) {
	for {
		client, changed := d.current()
		if client != nil {
			conn, err := client.DialContext(ctx, target)
			if err == nil {
				return conn, nil
			}
			if !isSessionRetryError(err) {
				return nil, err
			}
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (d *sessionDialer) OpenPacketContext(ctx context.Context) (net.PacketConn, error) {
	for {
		client, changed := d.current()
		if client != nil {
			packet, err := client.OpenPacketContext(ctx)
			if err == nil {
				return packet, nil
			}
			if !isSessionRetryError(err) {
				return nil, err
			}
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (d *sessionDialer) current() (sessionClient, <-chan struct{}) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.client, d.changed
}

func isSessionRetryError(err error) bool {
	return errors.Is(err, relay.ErrClientClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)
}
