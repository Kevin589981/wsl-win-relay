// Package framed provides a replaceable, frame-aware transport link.
// Attachments can be swapped without allowing a read to combine bytes from
// two transports in the middle of one protocol frame.
package framed

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
)

var (
	ErrDetached = errors.New("framed transport detached")
	ErrClosed   = errors.New("framed transport closed")
	ErrStale    = errors.New("framed transport attachment is stale")
	ErrNil      = errors.New("framed transport endpoint is nil")
)

type Link struct {
	mu      sync.Mutex
	current *Attachment
	changed chan struct{}
	closed  chan struct{}
	once    sync.Once
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func New() *Link {
	return &Link{changed: make(chan struct{}), closed: make(chan struct{})}
}

// Attach installs a new endpoint. Any previous endpoint is marked stale and
// closed to unblock a reader; errors from that old reader cannot detach the
// replacement because generations are compared by attachment identity.
func (l *Link) Attach(rw io.ReadWriter) (*Attachment, error) {
	if rw == nil {
		return nil, ErrNil
	}
	l.mu.Lock()
	select {
	case <-l.closed:
		l.mu.Unlock()
		return nil, ErrClosed
	default:
	}
	old := l.current
	a := &Attachment{link: l, rw: rw}
	l.current = a
	l.signalLocked()
	l.mu.Unlock()

	// Closing an old endpoint can run user transport code and may block. Keep
	// the link state available for concurrent readers and replacements.
	if old != nil {
		if closer, ok := old.rw.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	return a, nil
}

func (l *Link) ReadFrame() (protocol.Frame, error) {
	l.readMu.Lock()
	defer l.readMu.Unlock()
	for {
		a, wait, err := l.snapshot()
		if err != nil {
			return protocol.Frame{}, err
		}
		if wait != nil {
			select {
			case <-wait:
				continue
			case <-l.closed:
				return protocol.Frame{}, ErrClosed
			}
		}
		frame, err := protocol.Read(a.rw)
		if err == nil {
			return frame, nil
		}
		l.detachIfCurrent(a)
		return protocol.Frame{}, errors.Join(ErrDetached, err)
	}
}

func (l *Link) WriteFrame(frame protocol.Frame) error {
	return l.WriteFrameContext(context.Background(), frame)
}

func (l *Link) WriteFrameContext(ctx context.Context, frame protocol.Frame) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		a, wait, err := l.snapshot()
		if err != nil {
			return err
		}
		if wait != nil {
			select {
			case <-wait:
				continue
			case <-l.closed:
				return ErrClosed
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// Do not hold the write mutex while waiting for a replacement
		// attachment. Re-check the endpoint after acquiring it so a stale
		// snapshot cannot write into a newly replaced transport.
		l.writeMu.Lock()
		l.mu.Lock()
		current := l.current
		l.mu.Unlock()
		if current != a {
			l.writeMu.Unlock()
			continue
		}
		if err := protocol.Write(a.rw, frame); err != nil {
			l.writeMu.Unlock()
			l.detachIfCurrent(a)
			return errors.Join(ErrDetached, err)
		}
		l.writeMu.Unlock()
		return nil
	}
}

func (l *Link) snapshot() (*Attachment, <-chan struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
		return nil, nil, ErrClosed
	default:
	}
	if l.current == nil {
		return nil, l.changed, nil
	}
	return l.current, nil, nil
}

func (l *Link) signalLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *Link) detachIfCurrent(a *Attachment) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current != a {
		return
	}
	l.current = nil
	l.signalLocked()
}

func (l *Link) Close() error {
	l.once.Do(func() {
		l.mu.Lock()
		close(l.closed)
		current := l.current
		l.current = nil
		l.signalLocked()
		l.mu.Unlock()
		if current != nil {
			if closer, ok := current.rw.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	})
	return nil
}

type Attachment struct {
	link *Link
	rw   io.ReadWriter
	once sync.Once
	err  error
}

func (a *Attachment) Detach() error {
	a.once.Do(func() {
		a.link.mu.Lock()
		defer a.link.mu.Unlock()
		if a.link.current != a {
			a.err = ErrStale
			return
		}
		a.link.current = nil
		a.link.signalLocked()
	})
	return a.err
}

func (a *Attachment) Close() error {
	err := a.Detach()
	if closer, ok := a.rw.(io.Closer); ok {
		closeErr := closer.Close()
		if err == nil {
			err = closeErr
		}
	}
	return err
}

func (a *Attachment) Current() bool {
	a.link.mu.Lock()
	defer a.link.mu.Unlock()
	return a.link.current == a
}
