package relay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
)

var ErrClientClosed = errors.New("relay client is closed")

type Client struct {
	rw        io.ReadWriter
	writeMu   sync.Mutex
	mu        sync.Mutex
	streams   map[uint32]*clientStream
	listeners map[uint32]*clientListener
	nextID    atomic.Uint32
	closed    chan struct{}
	closeOne  sync.Once
	closeErr  error
}

func NewClient(rw io.ReadWriter) *Client {
	c := &Client{rw: rw, streams: make(map[uint32]*clientStream), listeners: make(map[uint32]*clientListener), closed: make(chan struct{})}
	c.nextID.Store(^uint32(0))
	return c
}

// Run reads and dispatches frames until the transport closes or ctx is canceled.
func (c *Client) Run(ctx context.Context) error {
	result := make(chan error, 1)
	go func() {
		for {
			frame, err := protocol.Read(c.rw)
			if err != nil {
				result <- err
				return
			}
			c.dispatch(frame)
		}
	}()
	select {
	case err := <-result:
		c.fail(err)
		return err
	case <-ctx.Done():
		c.fail(ctx.Err())
		return ctx.Err()
	case <-c.closed:
		return c.closeErr
	}
}

func (c *Client) DialContext(ctx context.Context, target string) (net.Conn, error) {
	if len(target) == 0 || len(target) > protocol.MaxTargetSize {
		return nil, fmt.Errorf("invalid target length")
	}
	id := c.nextID.Add(2)
	s := newClientStream(c, id, target)
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return nil, ErrClientClosed
	default:
	}
	c.streams[id] = s
	c.mu.Unlock()
	if err := c.write(protocol.Frame{Type: protocol.TypeOpen, StreamID: id, Payload: []byte(target)}); err != nil {
		c.removeStream(id)
		return nil, err
	}
	select {
	case err := <-s.openDone:
		if err != nil {
			c.removeStream(id)
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		_ = s.Close()
		return nil, ctx.Err()
	case <-c.closed:
		return nil, ErrClientClosed
	}
}

// ReverseForward asks the Windows side to listen on windowsAddr and forward
// accepted connections to the WSL-local target.
func (c *Client) ReverseForward(ctx context.Context, windowsAddr, target string) (io.Closer, error) {
	if windowsAddr == "" || target == "" || len(windowsAddr) > protocol.MaxTargetSize || len(target) > protocol.MaxTargetSize {
		return nil, fmt.Errorf("invalid reverse-forward address")
	}
	id := c.nextID.Add(2)
	l := &clientListener{id: id, client: c, target: target, ctx: ctx, ready: make(chan error, 1)}
	c.mu.Lock()
	c.listeners[id] = l
	c.mu.Unlock()
	if err := c.write(protocol.Frame{Type: protocol.TypeListenOpen, StreamID: id, Payload: []byte(windowsAddr)}); err != nil {
		c.removeListener(id)
		return nil, err
	}
	select {
	case err := <-l.ready:
		if err != nil {
			c.removeListener(id)
			return nil, err
		}
		return l, nil
	case <-ctx.Done():
		_ = l.Close()
		return nil, ctx.Err()
	case <-c.closed:
		return nil, ErrClientClosed
	}
}

func (c *Client) Close() error {
	c.fail(ErrClientClosed)
	return c.closeErr
}

func (c *Client) write(frame protocol.Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.Write(c.rw, frame)
}

func (c *Client) dispatch(frame protocol.Frame) {
	if frame.Type == protocol.TypeListenOK || frame.Type == protocol.TypeListenError {
		c.mu.Lock()
		l := c.listeners[frame.StreamID]
		c.mu.Unlock()
		if l != nil {
			if frame.Type == protocol.TypeListenOK {
				l.ready <- nil
			} else {
				l.ready <- errors.New(string(frame.Payload))
			}
		}
		return
	}
	if frame.Type == protocol.TypeInboundOpen {
		if len(frame.Payload) != 4 {
			return
		}
		listenerID := binary.BigEndian.Uint32(frame.Payload)
		c.mu.Lock()
		l := c.listeners[listenerID]
		c.mu.Unlock()
		if l != nil {
			go c.acceptInbound(l, frame.StreamID)
		}
		return
	}
	c.mu.Lock()
	s := c.streams[frame.StreamID]
	c.mu.Unlock()
	if s == nil {
		return
	}
	s.handle(frame)
}

func (c *Client) removeStream(id uint32) {
	c.mu.Lock()
	delete(c.streams, id)
	c.mu.Unlock()
}

func (c *Client) fail(err error) {
	c.closeOne.Do(func() {
		c.closeErr = err
		close(c.closed)
		c.mu.Lock()
		streams := make([]*clientStream, 0, len(c.streams))
		for _, s := range c.streams {
			streams = append(streams, s)
		}
		c.streams = make(map[uint32]*clientStream)
		c.listeners = make(map[uint32]*clientListener)
		c.mu.Unlock()
		for _, s := range streams {
			s.fail(err)
		}
	})
}

func (c *Client) removeListener(id uint32) { c.mu.Lock(); delete(c.listeners, id); c.mu.Unlock() }

func (c *Client) acceptInbound(l *clientListener, streamID uint32) {
	s := newClientStream(c, streamID, l.target)
	s.openOnce.Do(func() { s.openDone <- nil })
	c.mu.Lock()
	c.streams[streamID] = s
	c.mu.Unlock()
	local, err := (&net.Dialer{}).DialContext(l.ctx, "tcp", l.target)
	if err != nil {
		_ = c.write(protocol.Frame{Type: protocol.TypeReset, StreamID: streamID, Payload: []byte(err.Error())})
		c.removeStream(streamID)
		return
	}
	bridge(local, s)
	c.removeStream(streamID)
}

type clientListener struct {
	id     uint32
	client *Client
	target string
	ctx    context.Context
	ready  chan error
	once   sync.Once
}

func (l *clientListener) Close() error {
	l.once.Do(func() {
		_ = l.client.write(protocol.Frame{Type: protocol.TypeListenClose, StreamID: l.id})
		l.client.removeListener(l.id)
	})
	return nil
}

func bridge(a, b net.Conn) error {
	errCh := make(chan error, 2)
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
			_ = src.Close()
		}
		errCh <- err
	}
	go copyOne(a, b)
	go copyOne(b, a)
	err := <-errCh
	<-errCh
	_ = a.Close()
	_ = b.Close()
	return err
}

type clientStream struct {
	client        *Client
	id            uint32
	target        string
	incoming      chan streamEvent
	openDone      chan error
	openOnce      sync.Once
	closeOnce     sync.Once
	stateMu       sync.Mutex
	closedRemote  bool
	readBuf       []byte
	deadlineMu    sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

type streamEvent struct {
	data []byte
	err  error
}

func newClientStream(c *Client, id uint32, target string) *clientStream {
	return &clientStream{client: c, id: id, target: target, incoming: make(chan streamEvent, 64), openDone: make(chan error, 1)}
}

func (s *clientStream) handle(frame protocol.Frame) {
	switch frame.Type {
	case protocol.TypeOpenOK:
		s.openOnce.Do(func() { s.openDone <- nil })
	case protocol.TypeOpenError:
		s.openOnce.Do(func() { s.openDone <- errors.New(string(frame.Payload)) })
		s.fail(errors.New(string(frame.Payload)))
	case protocol.TypeData:
		select {
		case s.incoming <- streamEvent{data: append([]byte(nil), frame.Payload...)}:
		case <-s.client.closed:
		}
	case protocol.TypeHalfClose:
		select {
		case s.incoming <- streamEvent{err: io.EOF}:
		case <-s.client.closed:
		}
	case protocol.TypeClose, protocol.TypeReset:
		err := io.EOF
		if frame.Type == protocol.TypeReset {
			err = errors.New(string(frame.Payload))
		}
		s.fail(err)
	}
}

func (s *clientStream) fail(err error) {
	s.openOnce.Do(func() { s.openDone <- err })
	s.stateMu.Lock()
	if !s.closedRemote {
		s.closedRemote = true
		select {
		case s.incoming <- streamEvent{err: err}:
		default:
		}
	}
	s.stateMu.Unlock()
}

func (s *clientStream) Read(p []byte) (int, error) {
	for {
		if len(s.readBuf) > 0 {
			n := copy(p, s.readBuf)
			s.readBuf = s.readBuf[n:]
			return n, nil
		}
		s.deadlineMu.Lock()
		wait := s.readDeadline
		s.deadlineMu.Unlock()
		var timer <-chan time.Time
		var t *time.Timer
		if !wait.IsZero() {
			d := time.Until(wait)
			if d <= 0 {
				return 0, osTimeout{}
			}
			t = time.NewTimer(d)
			timer = t.C
		}
		select {
		case event := <-s.incoming:
			if t != nil {
				t.Stop()
			}
			if len(event.data) > 0 {
				s.readBuf = event.data
				continue
			}
			if event.err != nil {
				return 0, event.err
			}
		case <-timer:
			return 0, osTimeout{}
		case <-s.client.closed:
			return 0, ErrClientClosed
		}
	}
}

func (s *clientStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := len(p)
	for len(p) > 0 {
		s.deadlineMu.Lock()
		deadline := s.writeDeadline
		s.deadlineMu.Unlock()
		if !deadline.IsZero() && time.Now().After(deadline) {
			return 0, osTimeout{}
		}
		n := len(p)
		if n > protocol.MaxPayloadSize {
			n = protocol.MaxPayloadSize
		}
		if err := s.client.write(protocol.Frame{Type: protocol.TypeData, StreamID: s.id, Payload: p[:n]}); err != nil {
			return 0, err
		}
		p = p[n:]
	}
	return total, nil
}

func (s *clientStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.client.write(protocol.Frame{Type: protocol.TypeClose, StreamID: s.id})
		s.client.removeStream(s.id)
		s.fail(io.EOF)
	})
	return nil
}

// CloseWrite half-closes the client-to-Windows direction while keeping reads open.
func (s *clientStream) CloseWrite() error {
	return s.client.write(protocol.Frame{Type: protocol.TypeHalfClose, StreamID: s.id})
}

func (s *clientStream) LocalAddr() net.Addr  { return relayAddr("wsl") }
func (s *clientStream) RemoteAddr() net.Addr { return relayAddr(s.target) }
func (s *clientStream) SetDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.readDeadline, s.writeDeadline = t, t
	s.deadlineMu.Unlock()
	return nil
}
func (s *clientStream) SetReadDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.readDeadline = t
	s.deadlineMu.Unlock()
	return nil
}
func (s *clientStream) SetWriteDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.writeDeadline = t
	s.deadlineMu.Unlock()
	return nil
}

type relayAddr string

func (a relayAddr) Network() string { return "wsl-win-relay" }
func (a relayAddr) String() string  { return string(a) }

type osTimeout struct{}

func (osTimeout) Error() string   { return "i/o timeout" }
func (osTimeout) Timeout() bool   { return true }
func (osTimeout) Temporary() bool { return true }

var _ net.Conn = (*clientStream)(nil)
