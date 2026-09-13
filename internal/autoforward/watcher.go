package autoforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
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
	Scanner           DatagramScanner
	Opener            DatagramOpener
	WindowsHost       string
	WindowsHost6      string
	WindowsPortOffset int
	Interval          time.Duration
	OpenTimeout       time.Duration
	RetryMin          time.Duration
	RetryMax          time.Duration
	Included          map[uint16]bool
	Excluded          map[uint16]bool
	Logger            *log.Logger
	runnerMu          sync.Mutex
	runner            *Watcher
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
		WindowsHost6: w.WindowsHost6, WindowsPortOffset: w.WindowsPortOffset, Interval: w.Interval, OpenTimeout: w.OpenTimeout,
		RetryMin: w.RetryMin, RetryMax: w.RetryMax, Included: w.Included,
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
	Scanner           Scanner
	Opener            Opener
	WindowsHost       string
	WindowsHost6      string
	WindowsPortOffset int
	Interval          time.Duration
	OpenTimeout       time.Duration
	Included          map[uint16]bool
	Excluded          map[uint16]bool
	Logger            *log.Logger
	Label             string
	// RetryMin and RetryMax bound retries after a Windows mapping refusal.
	// Callers can expose these as deployment policy knobs.
	RetryMin      time.Duration
	RetryMax      time.Duration
	mu            sync.Mutex
	active        map[listenerKey]activeMapping
	rejected      map[listenerKey]bool
	retryAfter    map[listenerKey]time.Time
	retryFailures map[listenerKey]int
	generation    uint64
	attemptID     uint64
	attemptCancel context.CancelFunc
}

type listenerKey struct {
	network string
	port    uint16
}

type activeMapping struct {
	listener Listener
	closer   io.Closer
}

// Bound concurrent Windows bind/relay requests so a large procfs scan cannot
// consume unbounded goroutines while still avoiding head-of-line blocking.
const maxConcurrentOpens = 8

const (
	defaultRetryMin = time.Second
	defaultRetryMax = 30 * time.Second
)

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
	w.ensureRetryPolicy()
	w.mu.Lock()
	w.active = make(map[listenerKey]activeMapping)
	w.rejected = make(map[listenerKey]bool)
	w.retryAfter = make(map[listenerKey]time.Time)
	w.retryFailures = make(map[listenerKey]int)
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

func (w *Watcher) ensureRetryPolicy() {
	if w.RetryMin <= 0 {
		w.RetryMin = defaultRetryMin
	}
	if w.RetryMax <= 0 {
		w.RetryMax = defaultRetryMax
	}
	if w.RetryMax < w.RetryMin {
		w.RetryMax = w.RetryMin
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
	if w.retryAfter == nil {
		w.retryAfter = make(map[listenerKey]time.Time)
	}
	if w.retryFailures == nil {
		w.retryFailures = make(map[listenerKey]int)
	}
	var removed []activeMapping
	for key, mapping := range w.active {
		desiredListener, ok := desired[key]
		if !ok || desiredListener != mapping.listener {
			removed = append(removed, mapping)
			delete(w.active, key)
			delete(w.rejected, key)
			delete(w.retryAfter, key)
			delete(w.retryFailures, key)
			w.Logger.Printf("%s removed Windows port %d (%s)", w.Label, w.windowsPort(mapping.listener), mapping.listener.Network)
		}
	}
	w.mu.Unlock()
	for _, mapping := range removed {
		w.closeMapping(mapping.closer)
	}
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	w.mu.Lock()
	w.attemptID++
	attemptID := w.attemptID
	previousCancel := w.attemptCancel
	w.attemptCancel = cancelAttempt
	w.mu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
	defer func() {
		cancelAttempt()
		w.mu.Lock()
		if w.attemptID == attemptID {
			w.attemptCancel = nil
		}
		w.mu.Unlock()
	}()
	type mappingAttempt struct {
		key        listenerKey
		listener   Listener
		generation uint64
	}
	attempts := make([]mappingAttempt, 0, len(desired))
	for key, listener := range desired {
		w.mu.Lock()
		active, exists := w.active[key]
		retryAt := w.retryAfter[key]
		generation := w.generation
		w.mu.Unlock()
		if exists && active.listener == listener {
			continue
		}
		if !retryAt.IsZero() && time.Now().Before(retryAt) {
			continue
		}
		attempts = append(attempts, mappingAttempt{key: key, listener: listener, generation: generation})
	}
	sem := make(chan struct{}, maxConcurrentOpens)
	var attemptsDone sync.WaitGroup
	for _, attempt := range attempts {
		attemptsDone.Add(1)
		go func(attempt mappingAttempt) {
			defer attemptsDone.Done()
			select {
			case sem <- struct{}{}:
			case <-attemptCtx.Done():
				return
			}
			defer func() { <-sem }()
			w.openOne(attemptCtx, attempt.key, attempt.listener, attempt.generation, desired)
		}(attempt)
	}
	attemptsDone.Wait()
	return nil
}

func (w *Watcher) openOne(ctx context.Context, key listenerKey, listener Listener, generation uint64, desired map[listenerKey]Listener) {
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
	mappedPort := w.windowsPort(listener)
	var closer io.Closer
	var openErr error
	if mappedPort < 1 || mappedPort > 65535 {
		openErr = fmt.Errorf("WSL port %d plus Windows port offset %d is outside 1..65535", listener.Port, w.WindowsPortOffset)
	} else {
		windowsAddr = net.JoinHostPort(windowsHost, strconv.Itoa(mappedPort))
		closer, openErr = w.Opener.ReverseForward(openCtx, windowsAddr, wslTarget)
	}
	cancelOpen()
	w.mu.Lock()
	staleGeneration := generation != w.generation
	w.mu.Unlock()
	if staleGeneration {
		// Reset may have replaced the relay session while the opener was
		// blocked. Never publish a mapping created by that old session.
		w.closeMapping(closer)
		return
	}
	if ctx.Err() != nil {
		w.closeMapping(closer)
		return
	}
	if openErr != nil {
		// An opener may have created a Windows-side resource before reporting a
		// failure. Always close a non-nil handle returned with that error.
		w.closeMapping(closer)
		w.mu.Lock()
		firstRejection := !w.rejected[key]
		w.rejected[key] = true
		w.retryFailures[key]++
		failureCount := w.retryFailures[key]
		w.retryAfter[key] = time.Now().Add(w.retryDelay(failureCount))
		w.mu.Unlock()
		if firstRejection {
			w.Logger.Printf("%s rejected %s -> %s: %s", w.Label, windowsAddr, wslTarget, formatMappingRejection(openErr))
		}
		return
	}
	if closer == nil {
		openErr = errors.New("opener returned nil mapping")
		w.mu.Lock()
		firstRejection := !w.rejected[key]
		w.rejected[key] = true
		w.retryFailures[key]++
		failureCount := w.retryFailures[key]
		w.retryAfter[key] = time.Now().Add(w.retryDelay(failureCount))
		w.mu.Unlock()
		if firstRejection {
			w.Logger.Printf("%s rejected %s -> %s: %s", w.Label, windowsAddr, wslTarget, formatMappingRejection(openErr))
		}
		return
	}
	w.mu.Lock()
	wasRejected := w.rejected[key]
	delete(w.rejected, key)
	delete(w.retryAfter, key)
	delete(w.retryFailures, key)
	w.mu.Unlock()
	if wasRejected {
		w.Logger.Printf("%s restored %s -> %s", w.Label, windowsAddr, wslTarget)
	}
	var closeAfterUnlock io.Closer
	w.mu.Lock()
	desiredListener, stillDesired := desired[key]
	if generation == w.generation && stillDesired && desiredListener == listener {
		w.active[key] = activeMapping{listener: listener, closer: closer}
	} else {
		closeAfterUnlock = closer
	}
	w.mu.Unlock()
	if closeAfterUnlock != nil {
		w.closeMapping(closeAfterUnlock)
		return
	}
	w.Logger.Printf("%s added %s -> %s", w.Label, windowsAddr, wslTarget)
}

func (w *Watcher) windowsPort(listener Listener) int {
	return int(listener.Port) + w.WindowsPortOffset
}

func formatMappingRejection(err error) string {
	if err == nil {
		return "unknown Windows mapping error"
	}
	message := err.Error()
	lower := strings.ToLower(message)
	if strings.Contains(lower, "eaddrinuse") || strings.Contains(lower, "address already in use") || strings.Contains(lower, "only one usage") {
		return message + " (Windows refused the port; mirrored WSL networking may share the port namespace)"
	}
	return message
}

func (w *Watcher) retryDelay(failures int) time.Duration {
	if w.RetryMin <= 0 || w.RetryMax <= 0 {
		return 0
	}
	delay := w.RetryMin
	for index := 1; index < failures && delay < w.RetryMax; index++ {
		if delay > w.RetryMax/2 {
			return w.RetryMax
		}
		delay *= 2
	}
	if delay > w.RetryMax {
		return w.RetryMax
	}
	return delay
}

func (w *Watcher) closeAll() {
	w.mu.Lock()
	active := w.active
	w.active = make(map[listenerKey]activeMapping)
	w.retryAfter = make(map[listenerKey]time.Time)
	w.retryFailures = make(map[listenerKey]int)
	w.generation++
	cancelAttempt := w.attemptCancel
	w.attemptCancel = nil
	w.mu.Unlock()
	if cancelAttempt != nil {
		cancelAttempt()
	}
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
	w.retryAfter = make(map[listenerKey]time.Time)
	w.retryFailures = make(map[listenerKey]int)
	w.generation++
	cancelAttempt := w.attemptCancel
	w.attemptCancel = nil
	w.mu.Unlock()
	if cancelAttempt != nil {
		cancelAttempt()
	}
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
