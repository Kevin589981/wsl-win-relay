package autoforward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWatcherAddsAndRemovesListeners(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}, {{Network: "tcp4", Port: 8000}}, {}}}
	opener := &recordingOpener{closed: make(chan string, 1)}
	w := &Watcher{Scanner: scanner, Opener: opener, Interval: time.Millisecond, Included: map[uint16]bool{8000: true}, Excluded: map[uint16]bool{9000: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 1 || opener.opened[0] != "127.0.0.1:8000=127.0.0.1:8000" {
		t.Fatalf("opened: %v", opener.opened)
	}
	select {
	case got := <-opener.closed:
		if got != opener.opened[0] {
			t.Fatalf("closed %q", got)
		}
	default:
		t.Fatal("mapping was not closed")
	}
}

func TestWatcherReplacesChangedListenerIdentity(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{
		{{Network: "tcp4", Host: "127.0.0.1", Port: 8000}},
		{{Network: "tcp6", Host: "::1", Port: 8000}},
		{},
	}}
	opener := &recordingOpener{closed: make(chan string, 2)}
	w := &Watcher{Scanner: scanner, Opener: opener, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 2 {
		t.Fatalf("opened: %v", opener.opened)
	}
	if opener.opened[0] == opener.opened[1] {
		t.Fatalf("listener identity change did not replace mapping: %v", opener.opened)
	}
	closed := make(map[string]bool)
	for len(closed) < 2 {
		select {
		case value := <-opener.closed:
			closed[value] = true
		default:
			t.Fatalf("closed mappings: %v", closed)
		}
	}
	for _, value := range opener.opened {
		if !closed[value] {
			t.Fatalf("mapping %q was not closed", value)
		}
	}
}

func TestWatcherUsesIPv6WindowsHostForIPv6Listeners(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp6", Host: "::1", Port: 8000}}, {}}}
	opener := &recordingOpener{closed: make(chan string, 1)}
	w := &Watcher{Scanner: scanner, Opener: opener, WindowsHost: "127.0.0.1", WindowsHost6: "::", Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) == 0 || opener.opened[0] != "[::]:8000=[::1]:8000" {
		t.Fatalf("opened: %v", opener.opened)
	}
}

func TestWatcherAppliesWindowsPortOffset(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Host: "127.0.0.1", Port: 8000}}, {}}}
	opener := &recordingOpener{closed: make(chan string, 1)}
	w := &Watcher{Scanner: scanner, Opener: opener, WindowsPortOffset: 10000, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 1 || opener.opened[0] != "127.0.0.1:18000=127.0.0.1:8000" {
		t.Fatalf("opened: %v", opener.opened)
	}
}

func TestWatcherPublishesAndRemovesStatusFile(t *testing.T) {
	statusPath := t.TempDir() + "/mappings.json"
	scanner := fixedScanner{listeners: []Listener{{Network: "tcp4", Host: "127.0.0.1", Port: 8000}}}
	opener := &recordingOpener{closed: make(chan string, 1)}
	w := &Watcher{Scanner: scanner, Opener: opener, WindowsPortOffset: 10000, Status: NewStatusStore(statusPath), StatusOwner: "tcp", Interval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	var data []byte
	var document struct {
		Version   int             `json:"version"`
		ProcessID int             `json:"process_id"`
		UpdatedAt string          `json:"updated_at"`
		Mappings  []MappingStatus `json:"mappings"`
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		data, _ = os.ReadFile(statusPath)
		if json.Unmarshal(data, &document) == nil && len(document.Mappings) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(data) == 0 {
		cancel()
		<-done
		t.Fatal("status file was not published")
	}
	if err := json.Unmarshal(data, &document); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	if document.Version != 1 || document.ProcessID <= 0 || document.UpdatedAt == "" || len(document.Mappings) != 1 || document.Mappings[0].WindowsAddress != "127.0.0.1:18000" || document.Mappings[0].WSLAddress != "127.0.0.1:8000" {
		cancel()
		<-done
		t.Fatalf("status document: %#v", document)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statusPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status file remains after shutdown: %v", err)
	}
}

func TestStatusStoreMergesTCPAndUDPOwners(t *testing.T) {
	path := t.TempDir() + "/mappings.json"
	store := NewStatusStore(path)
	tcp := MappingStatus{Network: "tcp4", WindowsAddress: "127.0.0.1:18000", WSLAddress: "127.0.0.1:8000"}
	udp := MappingStatus{Network: "udp4", WindowsAddress: "127.0.0.1:15353", WSLAddress: "127.0.0.1:5353"}
	if err := store.Publish("tcp", []MappingStatus{tcp}); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish("udp", []MappingStatus{udp}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document mappingStatusDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Mappings) != 2 {
		t.Fatalf("merged mappings: %#v", document.Mappings)
	}
	if err := store.Clear("tcp"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Mappings) != 1 || document.Mappings[0] != udp {
		t.Fatalf("remaining UDP mapping: %#v", document.Mappings)
	}
	if err := store.Clear("udp"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("merged status file remains: %v", err)
	}
}

func TestStatusStoreDoesNotRewriteUnchangedSnapshot(t *testing.T) {
	path := t.TempDir() + "/mappings.json"
	store := NewStatusStore(path)
	mapping := MappingStatus{Network: "tcp4", WindowsAddress: "127.0.0.1:18000", WSLAddress: "127.0.0.1:8000"}
	if err := store.Publish("tcp", []MappingStatus{mapping}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := store.Publish("tcp", []MappingStatus{mapping}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("unchanged status snapshot was rewritten")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish("tcp", []MappingStatus{mapping}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("externally removed status file was not recreated: %v", err)
	}
}

func TestWatcherRejectsOutOfRangeWindowsPortOffset(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Host: "127.0.0.1", Port: 8000}}, {}}}
	opener := &recordingOpener{closed: make(chan string, 1)}
	w := &Watcher{Scanner: scanner, Opener: opener, WindowsPortOffset: 58000, Interval: time.Millisecond, Logger: log.New(io.Discard, "", 0)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 0 {
		t.Fatalf("out-of-range mapping was opened: %v", opener.opened)
	}
}

func TestWatcherUsesWindowsAllocatedPort(t *testing.T) {
	statusPath := t.TempDir() + "/mappings.json"
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Host: "127.0.0.1", Port: 8000}}, {}}}
	opener := &allocatedOpener{boundAddress: "127.0.0.1:49152", closed: make(chan string, 1)}
	var logs bytes.Buffer
	w := &Watcher{Scanner: scanner, Opener: opener, WindowsPortAuto: true, Status: NewStatusStore(statusPath), Interval: time.Millisecond, Logger: log.New(&logs, "", 0)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 1 || opener.opened[0] != "127.0.0.1:0=127.0.0.1:8000" {
		t.Fatalf("opened: %v", opener.opened)
	}
	if !strings.Contains(logs.String(), "removed Windows port 49152") {
		t.Fatalf("removal log did not use allocated port: %q", logs.String())
	}
}

func TestWatcherRejectsAutoPortWithoutBoundAddress(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}}}
	opener := &recordingOpener{closed: make(chan string, 1)}
	w := &Watcher{Scanner: scanner, Opener: opener, WindowsPortAuto: true, Logger: log.New(io.Discard, "", 0), active: make(map[listenerKey]activeMapping)}
	if err := w.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.active) != 0 {
		t.Fatalf("mapping without allocated address became active: %#v", w.active)
	}
	select {
	case <-opener.closed:
	default:
		t.Fatal("mapping without allocated address was not closed")
	}
}

func TestWatcherMapsBothFamiliesOnSamePort(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{
		{{Network: "tcp4", Host: "127.0.0.1", Port: 8000}, {Network: "tcp6", Host: "::1", Port: 8000}},
		{},
	}}
	opener := &recordingOpener{closed: make(chan string, 2)}
	w := &Watcher{Scanner: scanner, Opener: opener, WindowsHost: "127.0.0.1", WindowsHost6: "::1", Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, value := range opener.opened {
		got[value] = true
	}
	for _, want := range []string{"127.0.0.1:8000=127.0.0.1:8000", "[::1]:8000=[::1]:8000"} {
		if !got[want] {
			t.Fatalf("mapping %q missing from %#v", want, opener.opened)
		}
	}
}

func TestDatagramWatcherUsesUDPMappingLifecycle(t *testing.T) {
	scanner := &sequenceDatagramScanner{values: [][]Listener{{{Network: "udp4", Host: "127.0.0.1", Port: 5353}}, {}}}
	opener := &recordingDatagramOpener{closed: make(chan string, 1)}
	w := &DatagramWatcher{Scanner: scanner, Opener: opener, WindowsPortOffset: 1000, Interval: time.Millisecond, Included: map[uint16]bool{5353: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 1 || opener.opened[0] != "127.0.0.1:6353=127.0.0.1:5353" {
		t.Fatalf("opened: %v", opener.opened)
	}
	select {
	case got := <-opener.closed:
		if got != opener.opened[0] {
			t.Fatalf("closed %q", got)
		}
	default:
		t.Fatal("UDP mapping was not closed")
	}
}

func TestDatagramWatcherUsesIPv6WindowsHost(t *testing.T) {
	scanner := &sequenceDatagramScanner{values: [][]Listener{{{Network: "udp6", Host: "::", Port: 5353}}, {}}}
	opener := &recordingDatagramOpener{closed: make(chan string, 1)}
	w := &DatagramWatcher{Scanner: scanner, Opener: opener, WindowsHost: "127.0.0.1", WindowsHost6: "::1", Interval: time.Millisecond, Included: map[uint16]bool{5353: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 1 || opener.opened[0] != "[::1]:5353=[::1]:5353" {
		t.Fatalf("opened: %v", opener.opened)
	}
}

func TestWatcherDoesNotHoldStateLockWhileClosing(t *testing.T) {
	closeStarted := make(chan struct{})
	allowClose := make(chan struct{})
	opener := &blockingCloseOpener{started: closeStarted, allow: allowClose}
	w := &Watcher{Scanner: opener, Opener: opener, Logger: log.New(io.Discard, "", 0), active: map[listenerKey]activeMapping{
		{network: "tcp4", port: 8000}: {listener: Listener{Network: "tcp4", Port: 8000}, closer: opener},
	}}

	done := make(chan error, 1)
	go func() { done <- w.sync(context.Background()) }()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("close was not started")
	}
	w.mu.Lock()
	_, stillActive := w.active[listenerKey{network: "tcp4", port: 8000}]
	w.mu.Unlock()
	if stillActive {
		t.Fatal("removed mapping remained active while close was blocked")
	}
	close(allowClose)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatcherLogsRejectionOnceAndRestoration(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}, {{Network: "tcp4", Port: 8000}}, {{Network: "tcp4", Port: 8000}}}}
	opener := &recordingOpener{failures: 1, err: errors.New("address in use"), closed: make(chan string, 1)}
	var logs bytes.Buffer
	w := &Watcher{Scanner: scanner, Opener: opener, RetryMin: 0, RetryMax: 0, Logger: log.New(&logs, "", 0), active: make(map[listenerKey]activeMapping)}
	for i := 0; i < 3; i++ {
		if err := w.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(opener.opened) != 2 {
		t.Fatalf("opened: %v", opener.opened)
	}
	if got := strings.Count(logs.String(), "auto-forward rejected"); got != 1 {
		t.Fatalf("rejection log count=%d logs=%q", got, logs.String())
	}
	if got := strings.Count(logs.String(), "auto-forward restored"); got != 1 {
		t.Fatalf("restoration log count=%d logs=%q", got, logs.String())
	}
}

func TestFormatMappingRejectionExplainsSharedPortConflicts(t *testing.T) {
	got := formatMappingRejection(errors.New("listen tcp 127.0.0.1:8000: Only one usage of each socket address is normally permitted"))
	if !strings.Contains(got, "mirrored WSL networking") {
		t.Fatalf("diagnostic %q does not explain mirrored port conflicts", got)
	}
	if plain := formatMappingRejection(errors.New("connection reset")); plain != "connection reset" {
		t.Fatalf("non-bind error was rewritten: %q", plain)
	}
}

func TestWatcherBacksOffRejectedMapping(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}, {{Network: "tcp4", Port: 8000}}, {{Network: "tcp4", Port: 8000}}}}
	opener := &recordingOpener{failures: 1, err: errors.New("address in use"), closed: make(chan string, 1)}
	w := &Watcher{Scanner: scanner, Opener: opener, RetryMin: 30 * time.Millisecond, RetryMax: 30 * time.Millisecond, Logger: log.New(io.Discard, "", 0), active: make(map[listenerKey]activeMapping)}
	if err := w.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 1 {
		t.Fatalf("backoff did not suppress immediate retry: %v", opener.opened)
	}
	time.Sleep(35 * time.Millisecond)
	if err := w.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 2 {
		t.Fatalf("mapping was not retried after backoff: %v", opener.opened)
	}
}

func TestWatcherTreatsNilCloserAsRejected(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}, {{}}}}
	w := &Watcher{Scanner: scanner, Opener: nilCloserOpener{}, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherClosesHandleReturnedWithError(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}}}
	opener := &partialFailureOpener{closed: make(chan string, 1)}
	w := &Watcher{Scanner: scanner, Opener: opener, Logger: log.New(io.Discard, "", 0), active: make(map[listenerKey]activeMapping)}
	if err := w.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opener.closed:
	default:
		t.Fatal("partially-created mapping was not closed")
	}
}

func TestWatcherResetClosesActiveMappings(t *testing.T) {
	opener := &recordingOpener{closed: make(chan string, 1)}
	w := &Watcher{Opener: opener, Logger: log.New(io.Discard, "", 0), active: map[listenerKey]activeMapping{
		{network: "tcp4", port: 8000}: {listener: Listener{Network: "tcp4", Port: 8000}, closer: closeRecorder{value: "mapping", out: opener.closed}},
	}}
	w.Reset()
	select {
	case got := <-opener.closed:
		if got != "mapping" {
			t.Fatalf("closed %q", got)
		}
	default:
		t.Fatal("active mapping was not closed")
	}
}

func TestWatcherOpenTimeoutReleasesBlockedAttempt(t *testing.T) {
	w := &Watcher{
		Scanner:     &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}}},
		Opener:      timeoutOpener{},
		OpenTimeout: 10 * time.Millisecond,
		Logger:      log.New(io.Discard, "", 0),
		active:      make(map[listenerKey]activeMapping),
	}
	started := time.Now()
	if err := w.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked opener was not released: %s", elapsed)
	}
	w.mu.Lock()
	rejected := w.rejected[listenerKey{network: "tcp4", port: 8000}]
	w.mu.Unlock()
	if !rejected {
		t.Fatal("timed out mapping was not marked rejected")
	}
}

func TestWatcherDropsMappingOpenedBeforeReset(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}}}
	opener := &delayedOpenOpener{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	w := &Watcher{Scanner: scanner, Opener: opener, Logger: log.New(io.Discard, "", 0), active: make(map[listenerKey]activeMapping)}
	done := make(chan error, 1)
	go func() { done <- w.sync(context.Background()) }()
	select {
	case <-opener.started:
	case <-time.After(time.Second):
		t.Fatal("opener did not start")
	}
	w.Reset()
	close(opener.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-opener.closed:
	case <-time.After(time.Second):
		t.Fatal("stale mapping was not closed")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.active) != 0 {
		t.Fatalf("stale mapping was published: %#v", w.active)
	}
}

func TestWatcherResetCancelsInFlightAttempts(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}}}
	opener := &cancelAwareOpenOpener{started: make(chan struct{})}
	w := &Watcher{Scanner: scanner, Opener: opener, Logger: log.New(io.Discard, "", 0), active: make(map[listenerKey]activeMapping)}
	done := make(chan error, 1)
	go func() { done <- w.sync(context.Background()) }()
	select {
	case <-opener.started:
	case <-time.After(time.Second):
		t.Fatal("opener did not start")
	}
	w.Reset()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reset did not cancel in-flight opener")
	}
}

func TestWatcherOpensMappingsConcurrently(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{
		{Network: "tcp4", Port: 8000},
		{Network: "tcp4", Port: 8001},
	}}}
	opener := &parallelOpenOpener{started: make(chan string, 2), release: make(chan struct{})}
	w := &Watcher{Scanner: scanner, Opener: opener, Logger: log.New(io.Discard, "", 0), active: make(map[listenerKey]activeMapping)}
	done := make(chan error, 1)
	go func() { done <- w.sync(context.Background()) }()
	seen := make(map[string]bool)
	for len(seen) < 2 {
		select {
		case value := <-opener.started:
			seen[value] = true
		case <-time.After(time.Second):
			t.Fatalf("mapping attempts were serialized: %v", seen)
		}
	}
	close(opener.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type sequenceScanner struct {
	mu     sync.Mutex
	values [][]Listener
	at     int
}

type fixedScanner struct{ listeners []Listener }

func (s fixedScanner) Scan() ([]Listener, error) {
	return append([]Listener(nil), s.listeners...), nil
}

type sequenceDatagramScanner struct {
	mu     sync.Mutex
	values [][]Listener
	at     int
}

func (s *sequenceDatagramScanner) ScanDatagrams() ([]Listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at >= len(s.values) {
		return nil, nil
	}
	result := s.values[s.at]
	s.at++
	return result, nil
}

func (s *sequenceScanner) Scan() ([]Listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at >= len(s.values) {
		return nil, nil
	}
	result := s.values[s.at]
	s.at++
	return result, nil
}

type recordingOpener struct {
	mu       sync.Mutex
	opened   []string
	closed   chan string
	failures int
	err      error
}

type allocatedOpener struct {
	boundAddress string
	opened       []string
	closed       chan string
}

type allocatedCloser struct {
	address string
	closeRecorder
}

func (c allocatedCloser) BoundAddress() string { return c.address }

func (o *allocatedOpener) ReverseForward(_ context.Context, windows, wsl string) (io.Closer, error) {
	value := windows + "=" + wsl
	o.opened = append(o.opened, value)
	return allocatedCloser{address: o.boundAddress, closeRecorder: closeRecorder{value: value, out: o.closed}}, nil
}

type partialFailureOpener struct{ closed chan string }

func (o *partialFailureOpener) ReverseForward(_ context.Context, windows, wsl string) (io.Closer, error) {
	return closeRecorder{value: windows + "=" + wsl, out: o.closed}, errors.New("setup failed")
}

type nilCloserOpener struct{}

func (nilCloserOpener) ReverseForward(context.Context, string, string) (io.Closer, error) {
	return nil, nil
}

type timeoutOpener struct{}

func (timeoutOpener) ReverseForward(ctx context.Context, _, _ string) (io.Closer, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type delayedOpenOpener struct {
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
}

type cancelAwareOpenOpener struct {
	started chan struct{}
}

func (*cancelAwareOpenOpener) Scan() ([]Listener, error) { return nil, nil }

func (o *cancelAwareOpenOpener) ReverseForward(ctx context.Context, _, _ string) (io.Closer, error) {
	close(o.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*delayedOpenOpener) Scan() ([]Listener, error) { return nil, nil }

func (o *delayedOpenOpener) ReverseForward(context.Context, string, string) (io.Closer, error) {
	close(o.started)
	<-o.release
	return delayedCloser{closed: o.closed}, nil
}

type delayedCloser struct {
	closed chan struct{}
}

func (c delayedCloser) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

type parallelOpenOpener struct {
	started chan string
	release chan struct{}
}

func (*parallelOpenOpener) Scan() ([]Listener, error) { return nil, nil }

func (o *parallelOpenOpener) ReverseForward(_ context.Context, windows, _ string) (io.Closer, error) {
	_, port, _ := strings.Cut(windows, ":")
	o.started <- port
	if port == "8000" {
		<-o.release
	}
	return io.NopCloser(strings.NewReader("")), nil
}

type recordingDatagramOpener struct {
	mu       sync.Mutex
	opened   []string
	closed   chan string
	failures int
	err      error
}

func (o *recordingDatagramOpener) ReverseDatagramForward(_ context.Context, windows, wsl string) (io.Closer, error) {
	o.mu.Lock()
	value := windows + "=" + wsl
	o.opened = append(o.opened, value)
	if o.failures > 0 {
		o.failures--
		err := o.err
		o.mu.Unlock()
		return nil, err
	}
	o.mu.Unlock()
	return closeRecorder{value: value, out: o.closed}, nil
}

func (o *recordingOpener) ReverseForward(_ context.Context, windows, wsl string) (io.Closer, error) {
	o.mu.Lock()
	value := windows + "=" + wsl
	o.opened = append(o.opened, value)
	if o.failures > 0 {
		o.failures--
		err := o.err
		o.mu.Unlock()
		return nil, err
	}
	o.mu.Unlock()
	return closeRecorder{value: value, out: o.closed}, nil
}

type closeRecorder struct {
	value string
	out   chan string
}

type blockingCloseOpener struct {
	started chan struct{}
	allow   chan struct{}
}

func (o *blockingCloseOpener) Scan() ([]Listener, error) { return nil, nil }
func (o *blockingCloseOpener) ReverseForward(context.Context, string, string) (io.Closer, error) {
	return o, nil
}
func (o *blockingCloseOpener) Close() error {
	select {
	case <-o.started:
	default:
		close(o.started)
	}
	<-o.allow
	return nil
}

func (c closeRecorder) Close() error {
	select {
	case c.out <- c.value:
	default:
	}
	return nil
}
