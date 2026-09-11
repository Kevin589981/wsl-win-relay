package relay

import (
	"io"
	"sync"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
)

type flowWindow struct {
	mu        sync.Mutex
	available int
	notify    chan struct{}
}

func newFlowWindow() *flowWindow {
	return &flowWindow{available: protocol.InitialStreamWindow, notify: make(chan struct{}, 1)}
}

func (w *flowWindow) add(count uint32) bool {
	if count == 0 {
		return false
	}
	w.mu.Lock()
	if uint64(w.available)+uint64(count) > protocol.InitialStreamWindow {
		w.mu.Unlock()
		return false
	}
	w.available += int(count)
	w.mu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}
	return true
}

func (w *flowWindow) take(max int, done <-chan struct{}, deadline time.Time) (int, error) {
	for {
		w.mu.Lock()
		if w.available > 0 {
			count := max
			if count > w.available {
				count = w.available
			}
			w.available -= count
			w.mu.Unlock()
			return count, nil
		}
		w.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			duration := time.Until(deadline)
			if duration <= 0 {
				return 0, osTimeout{}
			}
			timer = time.NewTimer(duration)
			timeout = timer.C
		}
		select {
		case <-w.notify:
			if timer != nil {
				timer.Stop()
			}
		case <-done:
			if timer != nil {
				timer.Stop()
			}
			return 0, io.ErrClosedPipe
		case <-timeout:
			return 0, osTimeout{}
		}
	}
}
