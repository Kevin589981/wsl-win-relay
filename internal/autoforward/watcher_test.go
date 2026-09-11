package autoforward

import (
	"context"
	"io"
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

type sequenceScanner struct {
	mu     sync.Mutex
	values [][]Listener
	at     int
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
	mu     sync.Mutex
	opened []string
	closed chan string
}

func (o *recordingOpener) ReverseForward(_ context.Context, windows, wsl string) (io.Closer, error) {
	o.mu.Lock()
	value := windows + "=" + wsl
	o.opened = append(o.opened, value)
	o.mu.Unlock()
	return closeRecorder{value: value, out: o.closed}, nil
}

type closeRecorder struct {
	value string
	out   chan string
}

func (c closeRecorder) Close() error {
	select {
	case c.out <- c.value:
	default:
	}
	return nil
}
