package autoforward

import (
	"context"
	"errors"
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
type DatagramScanner interface{ ScanDatagrams() ([]Listener, error) }
type DatagramOpener interface {
	ReverseDatagramForward(context.Context, string, string) (io.Closer, error)
}

// DatagramWatcher reuses the TCP mapping lifecycle with a UDP-specific procfs
// scanner and opener. It is intentionally a separate opt-in adapter because
// procfs cannot prove that an unconnected UDP socket is a server.
type DatagramWatcher struct {
	Scanner      DatagramScanner
	Opener       DatagramOpener
	WindowsHost  string
	WindowsHost6 string
	Interval     time.Duration
	OpenTimeout  time.Duration
	Included     map[uint16]bool
	Excluded     map[uint16]bool
	Logger       *log.Logger
	runnerMu     sync.Mutex
	runner       *Watcher
}

type datagramScannerAdapter struct{ scanner DatagramScanner }

func (a datagramScannerAdapter) Scan() ([]Listener, error) { return a.scanner.ScanDatagrams() }

type datagramOpenerAdapter struct{ opener DatagramOpener }

func (a datagramOpenerAdapter) ReverseForward(ctx context.Context, windows, wsl string) (io.Closer, error) {
	return a.opener.ReverseDatagramForward(ctx, windows, wsl)
}

func (w *DatagramWatcher) Run(ctx context.Context) error {
	if w.Scanner == nil || w.Opener == nil {
		return fmt.Errorf("datagram scanner and opener are required")
	}
	runner := &Watcher{
		Scanner: w.datagramScanner(), Opener: w.datagramOpener(), WindowsHost: w.WindowsHost,
		WindowsHost6: w.WindowsHost6, Interval: w.Interval, OpenTimeout: w.OpenTimeout, Included: w.Included,
		Excluded: w.Excluded, Logger: w.Logger, Label: "auto-forward UDP",
	}
	w.runnerMu.Lock()
	w.runner = runner
	w.runnerMu.Unlock()
	defer func() {
		w.runnerMu.Lock()
		w.runner = nil
		w.runnerMu.Unlock()
	}()
	return runner.Run(ctx)
}

// Reset drops mappings owned by the previous relay session. The next scan
// will recreate them through the current opener.
func (w *DatagramWatcher) Reset() {
	w.runnerMu.Lock()
	runner := w.runner
	w.runnerMu.Unlock()
	if runner != nil {
		runner.Reset()
	}
}

func (w *DatagramWatcher) datagramScanner() Scanner {
	return datagramScannerAdapter{scanner: w.Scanner}
}
func (w *DatagramWatcher) datagramOpener() Opener { return datagramOpenerAdapter{opener: w.Opener} }

type Watcher struct {
	Scanner      Scanner
	Opener       Opener
	WindowsHost  string
	WindowsHost6 string
	Interval     time.Duration
	OpenTimeout  time.Duration
	Included     map[uint16]bool
	Excluded     map[uint16]bool
	Logger       *log.Logger
	Label        string
	mu           sync.Mutex
	active       map[listenerKey]activeMapping
	rejected     map[listenerKey]bool
}

type listenerKey struct {
	network string
	port    uint16
}

type activeMapping struct {
	listener Listener
	closer   io.Closer
}

func (w *Watcher) Run(ctx context.Context) error {
	if w.Scanner == nil || w.Opener == nil {
		return fmt.Errorf("scanner and opener are required")
	}
	if w.WindowsHost == "" {
		w.WindowsHost = "127.0.0.1"
	}
	if w.WindowsHost6 == "" {
		w.WindowsHost6 = "::1"
	}
	if w.Interval <= 0 {
		w.Interval = time.Second
	}
	if w.Logger == nil {
		w.Logger = log.New(io.Discard, "", 0)
	}
	if w.Label == "" {
		w.Label = "auto-forward"
	}
	w.mu.Lock()
	w.active = make(map[listenerKey]activeMapping)
	w.rejected = make(map[listenerKey]bool)
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
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if w.Label == "" {
		w.Label = "auto-forward"
	}
	listeners, err := w.Scanner.Scan()
	if err != nil {
		return err
	}
	desired := make(map[listenerKey]Listener)
	for _, listener := range listeners {
		if len(w.Included) > 0 && !w.Included[listener.Port] {
			continue
		}
		if w.Excluded[listener.Port] {
			continue
		}
		key := listenerKey{network: listener.Network, port: listener.Port}
		desired[key] = listener
	}
	w.mu.Lock()
	if w.rejected == nil {
		w.rejected = make(map[listenerKey]bool)
	}
	var removed []activeMapping
	for key, mapping := range w.active {
		desiredListener, ok := desired[key]
		if !ok || desiredListener != mapping.listener {
			removed = append(removed, mapping)
			delete(w.active, key)
			delete(w.rejected, key)
			w.Logger.Printf("%s removed Windows port %d (%s)", w.Label, mapping.listener.Port, mapping.listener.Network)
		}
	}
	w.mu.Unlock()
	for _, mapping := range removed {
		w.closeMapping(mapping.closer)
	}
	for key, listener := range desired {
		w.mu.Lock()
		active, exists := w.active[key]
		w.mu.Unlock()
		if exists && active.listener == listener {
			continue
		}
		windowsHost := w.WindowsHost
		wslHost := listener.Host
		if listener.Network == "tcp6" {
			windowsHost = w.WindowsHost6
		}
		if listener.Network == "tcp4" && (wslHost == "" || wslHost == "0.0.0.0") {
			wslHost = "127.0.0.1"
		}
		if listener.Network == "tcp6" && (wslHost == "" || wslHost == "::") {
			wslHost = "::1"
		}
		windowsAddr := net.JoinHostPort(windowsHost, strconv.Itoa(int(listener.Port)))
		wslTarget := net.JoinHostPort(wslHost, strconv.Itoa(int(listener.Port)))
		openCtx := ctx
		cancelOpen := func() {}
		if w.OpenTimeout > 0 {
			openCtx, cancelOpen = context.WithTimeout(ctx, w.OpenTimeout)
		}
		closer, openErr := w.Opener.ReverseForward(openCtx, windowsAddr, wslTarget)
		cancelOpen()
		if openErr != nil {
			w.mu.Lock()
			firstRejection := !w.rejected[key]
			w.rejected[key] = true
			w.mu.Unlock()
			if firstRejection {
				w.Logger.Printf("%s rejected %s -> %s: %v", w.Label, windowsAddr, wslTarget, openErr)
			}
			continue
		}
		if closer == nil {
			openErr = errors.New("opener returned nil mapping")
			w.mu.Lock()
			firstRejection := !w.rejected[key]
			w.rejected[key] = true
			w.mu.Unlock()
			if firstRejection {
				w.Logger.Printf("%s rejected %s -> %s: %v", w.Label, windowsAddr, wslTarget, openErr)
			}
			continue
		}
		w.mu.Lock()
		wasRejected := w.rejected[key]
		delete(w.rejected, key)
		w.mu.Unlock()
		if wasRejected {
			w.Logger.Printf("%s restored %s -> %s", w.Label, windowsAddr, wslTarget)
		}
		var closeAfterUnlock io.Closer
		w.mu.Lock()
		if _, stillDesired := desired[key]; stillDesired {
			w.active[key] = activeMapping{listener: listener, closer: closer}
		} else {
			closeAfterUnlock = closer
		}
		w.mu.Unlock()
		if closeAfterUnlock != nil {
			w.closeMapping(closeAfterUnlock)
		}
		w.Logger.Printf("%s added %s -> %s", w.Label, windowsAddr, wslTarget)
	}
	return nil
}

func (w *Watcher) closeAll() {
	w.mu.Lock()
	active := w.active
	w.active = make(map[listenerKey]activeMapping)
	w.mu.Unlock()
	for _, mapping := range active {
		w.closeMapping(mapping.closer)
	}
}

// Reset closes all mappings without stopping the watcher. It is used when the
// relay transport is replaced so active entries cannot retain dead clients.
func (w *Watcher) Reset() {
	w.mu.Lock()
	active := w.active
	w.active = make(map[listenerKey]activeMapping)
	w.rejected = make(map[listenerKey]bool)
	w.mu.Unlock()
	for _, mapping := range active {
		w.closeMapping(mapping.closer)
	}
}

func (w *Watcher) closeMapping(closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		w.Logger.Printf("%s close: %v", w.Label, err)
	}
}
