package autoforward

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
)

type Scanner interface{ Scan() ([]Listener, error) }
type Opener interface {
	ReverseForward(context.Context, string, string) (io.Closer, error)
}

type Watcher struct {
	Scanner     Scanner
	Opener      Opener
	WindowsHost string
	Interval    time.Duration
	Included    map[uint16]bool
	Excluded    map[uint16]bool
	Logger      *log.Logger
	mu          sync.Mutex
	active      map[uint16]io.Closer
}

func (w *Watcher) Run(ctx context.Context) error {
	if w.Scanner == nil || w.Opener == nil {
		return fmt.Errorf("scanner and opener are required")
	}
	if w.WindowsHost == "" {
		w.WindowsHost = "127.0.0.1"
	}
	if w.Interval <= 0 {
		w.Interval = time.Second
	}
	if w.Logger == nil {
		w.Logger = log.New(io.Discard, "", 0)
	}
	w.mu.Lock()
	w.active = make(map[uint16]io.Closer)
	w.mu.Unlock()
	defer w.closeAll()
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		if err := w.sync(ctx); err != nil {
			w.Logger.Printf("auto-forward scan: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Watcher) sync(ctx context.Context) error {
	listeners, err := w.Scanner.Scan()
	if err != nil {
		return err
	}
	desired := make(map[uint16]Listener)
	for _, listener := range listeners {
		if len(w.Included) > 0 && !w.Included[listener.Port] {
			continue
		}
		if w.Excluded[listener.Port] {
			continue
		}
		desired[listener.Port] = listener
	}
	w.mu.Lock()
	for port, closer := range w.active {
		if _, ok := desired[port]; !ok {
			_ = closer.Close()
			delete(w.active, port)
			w.Logger.Printf("auto-forward removed Windows port %d", port)
		}
	}
	w.mu.Unlock()
	for port, listener := range desired {
		w.mu.Lock()
		_, exists := w.active[port]
		w.mu.Unlock()
		if exists {
			continue
		}
		windowsAddr := net.JoinHostPort(w.WindowsHost, strconv.Itoa(int(port)))
		wslHost := listener.Host
		if listener.Network == "tcp4" && (wslHost == "" || wslHost == "0.0.0.0") {
			wslHost = "127.0.0.1"
		}
		if listener.Network == "tcp6" && (wslHost == "" || wslHost == "::") {
			wslHost = "::1"
		}
		wslTarget := net.JoinHostPort(wslHost, strconv.Itoa(int(port)))
		closer, openErr := w.Opener.ReverseForward(ctx, windowsAddr, wslTarget)
		if openErr != nil {
			w.Logger.Printf("auto-forward rejected %s -> %s: %v", windowsAddr, wslTarget, openErr)
			continue
		}
		w.mu.Lock()
		if _, stillDesired := desired[port]; stillDesired {
			w.active[port] = closer
		} else {
			_ = closer.Close()
		}
		w.mu.Unlock()
		w.Logger.Printf("auto-forward added %s -> %s", windowsAddr, wslTarget)
	}
	return nil
}

func (w *Watcher) closeAll() {
	w.mu.Lock()
	active := w.active
	w.active = make(map[uint16]io.Closer)
	w.mu.Unlock()
	for _, closer := range active {
		_ = closer.Close()
	}
}
