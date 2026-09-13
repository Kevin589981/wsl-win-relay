package netserve

import (
	"context"
	"errors"
	"net"
	"sync"
)

const DefaultMaxConnections = 256

// Serve accepts connections until ctx is canceled or the listener fails.
// Accepted connections are owned by this loop and are closed and drained
// before Serve returns.
func Serve(ctx context.Context, listener net.Listener, handler func(context.Context, net.Conn)) error {
	return ServeWithLimit(ctx, listener, DefaultMaxConnections, handler)
}

// ServeWithLimit closes newly accepted connections while maxConnections
// handlers are active. Existing handlers retain their connections and slots.
func ServeWithLimit(ctx context.Context, listener net.Listener, maxConnections int, handler func(context.Context, net.Conn)) error {
	if listener == nil || handler == nil {
		return errors.New("listener and handler are required")
	}
	if maxConnections <= 0 {
		return errors.New("maximum connections must be positive")
	}
	serveCtx, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	closeConnections := func() {
		mu.Lock()
		current := make([]net.Conn, 0, len(connections))
		for conn := range connections {
			current = append(current, conn)
		}
		mu.Unlock()
		for _, conn := range current {
			_ = conn.Close()
		}
	}
	defer func() {
		cancel()
		_ = listener.Close()
		closeConnections()
		handlers.Wait()
	}()
	go func() {
		<-serveCtx.Done()
		_ = listener.Close()
		closeConnections()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if serveCtx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		if len(connections) >= maxConnections {
			mu.Unlock()
			_ = conn.Close()
			continue
		}
		connections[conn] = struct{}{}
		handlers.Add(1)
		mu.Unlock()
		go func() {
			defer func() {
				_ = conn.Close()
				mu.Lock()
				delete(connections, conn)
				mu.Unlock()
				handlers.Done()
			}()
			handler(serveCtx, conn)
		}()
	}
}
