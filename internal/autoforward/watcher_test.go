package autoforward

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
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
	w := &DatagramWatcher{Scanner: scanner, Opener: opener, Interval: time.Millisecond, Included: map[uint16]bool{5353: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(opener.opened) != 1 || opener.opened[0] != "127.0.0.1:5353=127.0.0.1:5353" {
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
	w := &Watcher{Scanner: scanner, Opener: opener, Logger: log.New(&logs, "", 0), active: make(map[listenerKey]activeMapping)}
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

func TestWatcherTreatsNilCloserAsRejected(t *testing.T) {
	scanner := &sequenceScanner{values: [][]Listener{{{Network: "tcp4", Port: 8000}}, {{}}}}
	w := &Watcher{Scanner: scanner, Opener: nilCloserOpener{}, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
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

type sequenceScanner struct {
	mu     sync.Mutex
	values [][]Listener
	at     int
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
