package autoforward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Scanner interface{ Scan() ([]Listener, error) }
type Opener interface {
	ReverseForward(context.Context, string, string) (io.Closer, error)
}
type DatagramScanner interface{ ScanDatagrams() ([]Listener, error) }
type DatagramOpener interface {
	ReverseDatagramForward(context.Context, string, string) (io.Closer, error)
}

// MappingStatus is the machine-readable representation of one desired
// automatic mapping and its current Windows registration state.
type MappingStatus struct {
	Network        string `json:"network"`
	WindowsAddress string `json:"windows_address"`
	WSLAddress     string `json:"wsl_address"`
	State          string `json:"state"`
	Error          string `json:"error,omitempty"`
	RetryAt        string `json:"retry_at,omitempty"`
}

type StatusDocument struct {
	Version   int             `json:"version"`
	ProcessID int             `json:"process_id"`
	UpdatedAt string          `json:"updated_at"`
	Mappings  []MappingStatus `json:"mappings"`
}

// StatusStore merges mapping snapshots from the TCP and UDP watchers before
// publishing one atomic status document.
type StatusStore struct {
	path      string
	processID int
	mu        sync.Mutex
	owners    map[string][]MappingStatus
	last      []MappingStatus
	hasLast   bool
	lastWrite time.Time
	now       func() time.Time
}

func NewStatusStore(path string) *StatusStore {
	return &StatusStore{path: path, processID: os.Getpid(), owners: make(map[string][]MappingStatus), now: time.Now}
}

func (s *StatusStore) Publish(owner string, mappings []MappingStatus) error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owners == nil {
		s.owners = make(map[string][]MappingStatus)
	}
	s.owners[owner] = append([]MappingStatus(nil), mappings...)
	return s.writeLocked()
}

func (s *StatusStore) Clear(owner string) error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.owners, owner)
	if len(s.owners) == 0 {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.last = nil
		s.hasLast = false
		s.lastWrite = time.Time{}
		return nil
	}
	return s.writeLocked()
}

func (s *StatusStore) writeLocked() error {
	mappings := make([]MappingStatus, 0)
	for _, ownerMappings := range s.owners {
		mappings = append(mappings, ownerMappings...)
	}
	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].Network != mappings[j].Network {
			return mappings[i].Network < mappings[j].Network
		}
		if mappings[i].WindowsAddress != mappings[j].WindowsAddress {
			return mappings[i].WindowsAddress < mappings[j].WindowsAddress
		}
		if mappings[i].WSLAddress != mappings[j].WSLAddress {
			return mappings[i].WSLAddress < mappings[j].WSLAddress
		}
		return mappings[i].State < mappings[j].State
	})
	for index, mapping := range mappings {
		if err := validateMappingStatus(mapping); err != nil {
			return fmt.Errorf("mapping %d: %w", index, err)
		}
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	if s.hasLast && equalMappingStatuses(s.last, mappings) && !now.Before(s.lastWrite) && now.Sub(s.lastWrite) < StatusHeartbeatInterval {
		if _, err := os.Stat(s.path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	data, err := json.MarshalIndent(StatusDocument{Version: StatusVersion, ProcessID: s.processID, UpdatedAt: now.UTC().Format(time.RFC3339Nano), Mappings: mappings}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeStatusFile(s.path, append(data, '\n')); err != nil {
		return err
	}
	s.last = append(s.last[:0], mappings...)
	s.hasLast = true
	s.lastWrite = now
	return nil
}

func equalMappingStatuses(left, right []MappingStatus) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
	WindowsPortAuto   bool
	Status            *StatusStore
	StatusOwner       string
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
		WindowsHost6: w.WindowsHost6, WindowsPortOffset: w.WindowsPortOffset, WindowsPortAuto: w.WindowsPortAuto, Status: w.Status, StatusOwner: "udp", Interval: w.Interval, OpenTimeout: w.OpenTimeout,
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
	WindowsPortAuto   bool
	Status            *StatusStore
	StatusOwner       string
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
	rejectedAt    map[listenerKey]Listener
	rejectedError map[listenerKey]string
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
	listener       Listener
	windowsAddress string
	wslAddress     string
	closer         io.Closer
}

// Bound concurrent Windows bind/relay requests so a large procfs scan cannot
// consume unbounded goroutines while still avoiding head-of-line blocking.
const maxConcurrentOpens = 8

const (
	defaultRetryMin = time.Second
	defaultRetryMax = 30 * time.Second
	maxStatusError  = 4096
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
	if w.StatusOwner == "" {
		w.StatusOwner = w.Label
	}
	w.ensureRetryPolicy()
	w.mu.Lock()
	w.active = make(map[listenerKey]activeMapping)
	w.rejected = make(map[listenerKey]bool)
	w.rejectedAt = make(map[listenerKey]Listener)
	w.rejectedError = make(map[listenerKey]string)
	w.retryAfter = make(map[listenerKey]time.Time)
	w.retryFailures = make(map[listenerKey]int)
	w.mu.Unlock()
	defer func() {
		w.closeAll()
		w.clearStatus()
	}()
	w.publishStatus()
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
	if w.rejectedAt == nil {
		w.rejectedAt = make(map[listenerKey]Listener)
	}
	if w.rejectedError == nil {
		w.rejectedError = make(map[listenerKey]string)
	}
	if w.retryFailures == nil {
		w.retryFailures = make(map[listenerKey]int)
	}
	var removed []activeMapping
	statusChanged := false
	for key, mapping := range w.active {
		desiredListener, ok := desired[key]
		if !ok || desiredListener != mapping.listener {
			removed = append(removed, mapping)
			delete(w.active, key)
			delete(w.rejected, key)
			delete(w.rejectedAt, key)
			delete(w.rejectedError, key)
			delete(w.retryAfter, key)
			delete(w.retryFailures, key)
			statusChanged = true
			_, rawPort, _ := net.SplitHostPort(mapping.windowsAddress)
			w.Logger.Printf("%s removed Windows port %s (%s)", w.Label, rawPort, mapping.listener.Network)
		}
	}
	for key, rejectedListener := range w.rejectedAt {
		desiredListener, stillDesired := desired[key]
		if stillDesired && desiredListener == rejectedListener {
			continue
		}
		delete(w.rejected, key)
		delete(w.rejectedAt, key)
		delete(w.rejectedError, key)
		delete(w.retryAfter, key)
		delete(w.retryFailures, key)
		statusChanged = true
	}
	w.mu.Unlock()
	for _, mapping := range removed {
		w.closeMapping(mapping.closer)
	}
	if statusChanged {
		w.publishStatus()
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
	w.publishStatus()
	return nil
}

func (w *Watcher) openOne(ctx context.Context, key listenerKey, listener Listener, generation uint64, desired map[listenerKey]Listener) {
	windowsAddr, wslTarget := w.mappingAddresses(listener)
	openCtx := ctx
	cancelOpen := func() {}
	if w.OpenTimeout > 0 {
		openCtx, cancelOpen = context.WithTimeout(ctx, w.OpenTimeout)
	}
	mappedPort := w.windowsPort(listener)
	var closer io.Closer
	var openErr error
	if !w.WindowsPortAuto && (mappedPort < 1 || mappedPort > 65535) {
		openErr = fmt.Errorf("WSL port %d plus Windows port offset %d is outside 1..65535", listener.Port, w.WindowsPortOffset)
	} else {
		closer, openErr = w.Opener.ReverseForward(openCtx, windowsAddr, wslTarget)
		if openErr == nil && w.WindowsPortAuto {
			bound, ok := closer.(interface{ BoundAddress() string })
			if !ok {
				openErr = errors.New("relay did not report the Windows-allocated port")
			} else if addressErr := validateAllocatedAddress(bound.BoundAddress()); addressErr != nil {
				openErr = addressErr
			} else {
				windowsAddr = bound.BoundAddress()
			}
		}
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
		w.rejectedAt[key] = listener
		w.rejectedError[key] = boundedStatusError(openErr)
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
		w.rejectedAt[key] = listener
		w.rejectedError[key] = boundedStatusError(openErr)
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
	delete(w.rejectedAt, key)
	delete(w.rejectedError, key)
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
		w.active[key] = activeMapping{listener: listener, windowsAddress: windowsAddr, wslAddress: wslTarget, closer: closer}
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

func boundedStatusError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) <= maxStatusError {
		return message
	}
	message = message[:maxStatusError]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func (w *Watcher) mappingAddresses(listener Listener) (string, string) {
	windowsHost := w.WindowsHost
	wslHost := listener.Host
	if strings.HasSuffix(listener.Network, "6") {
		windowsHost = w.WindowsHost6
		if wslHost == "" || wslHost == "::" {
			wslHost = "::1"
		}
	} else if strings.HasSuffix(listener.Network, "4") && (wslHost == "" || wslHost == "0.0.0.0") {
		wslHost = "127.0.0.1"
	}
	windowsAddr := net.JoinHostPort(windowsHost, strconv.Itoa(w.windowsPort(listener)))
	wslTarget := net.JoinHostPort(wslHost, strconv.Itoa(int(listener.Port)))
	return windowsAddr, wslTarget
}

func (w *Watcher) windowsPort(listener Listener) int {
	if w.WindowsPortAuto {
		return 0
	}
	return int(listener.Port) + w.WindowsPortOffset
}

func validateAllocatedAddress(address string) error {
	_, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("relay returned invalid Windows bound address %q: %w", address, err)
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("relay returned invalid Windows bound address %q", address)
	}
	return nil
}

func (w *Watcher) publishStatus() {
	if w.Status == nil {
		return
	}
	w.mu.Lock()
	mappings := make([]MappingStatus, 0, len(w.active))
	for _, mapping := range w.active {
		mappings = append(mappings, MappingStatus{Network: mapping.listener.Network, WindowsAddress: mapping.windowsAddress, WSLAddress: mapping.wslAddress, State: "active"})
	}
	for key, listener := range w.rejectedAt {
		if _, active := w.active[key]; active {
			continue
		}
		windowsAddr, wslAddr := w.mappingAddresses(listener)
		retryAt := ""
		if value := w.retryAfter[key]; !value.IsZero() {
			retryAt = value.UTC().Format(time.RFC3339Nano)
		}
		mappings = append(mappings, MappingStatus{Network: listener.Network, WindowsAddress: windowsAddr, WSLAddress: wslAddr, State: "rejected", Error: w.rejectedError[key], RetryAt: retryAt})
	}
	w.mu.Unlock()
	err := w.Status.Publish(w.StatusOwner, mappings)
	if err != nil && w.Logger != nil {
		w.Logger.Printf("%s status file: %v", w.Label, err)
	}
}

func writeStatusFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	base := filepath.Base(path)
	temporary, err := os.CreateTemp(directory, "."+base+".tmp-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func (w *Watcher) clearStatus() {
	if w.Status == nil {
		return
	}
	if err := w.Status.Clear(w.StatusOwner); err != nil && w.Logger != nil {
		w.Logger.Printf("%s status file cleanup: %v", w.Label, err)
	}
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
	w.rejectedAt = make(map[listenerKey]Listener)
	w.rejectedError = make(map[listenerKey]string)
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
	w.rejectedAt = make(map[listenerKey]Listener)
	w.rejectedError = make(map[listenerKey]string)
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
	w.publishStatus()
}

func (w *Watcher) closeMapping(closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		w.Logger.Printf("%s close: %v", w.Label, err)
	}
}
