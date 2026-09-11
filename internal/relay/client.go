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
var ErrClientAlreadyRunning = errors.New("relay client is already running")
var ErrMissingCapabilities = errors.New("Windows relay is missing required capabilities")

type Client struct {
	rw                io.ReadWriter
	writeMu           sync.Mutex
	mu                sync.Mutex
	streams           map[uint32]*clientStream
	listeners         map[uint32]*clientListener
	datagrams         map[uint32]*clientPacketConn
	nextID            atomic.Uint32
	closed            chan struct{}
	closeOne          sync.Once
	transportCloseOne sync.Once
	closeErr          error
	runOnce           sync.Once
	helloDone         chan helloResult
	helloOnce         sync.Once
	handshakeMu       sync.Mutex
	peerCapabilities  uint64
	handshakeComplete bool
}

type helloResult struct {
	capabilities uint64
	err          error
}

func NewClient(rw io.ReadWriter) *Client {
	c := &Client{rw: rw, streams: make(map[uint32]*clientStream), listeners: make(map[uint32]*clientListener), datagrams: make(map[uint32]*clientPacketConn), closed: make(chan struct{}), helloDone: make(chan helloResult, 1)}
	c.nextID.Store(^uint32(0))
	return c
}

func (c *Client) Handshake(ctx context.Context, required uint64) (uint64, error) {
	c.handshakeMu.Lock()
	defer c.handshakeMu.Unlock()
	if c.handshakeComplete {
		if c.peerCapabilities&required != required {
			return c.peerCapabilities, fmt.Errorf("%w: required 0x%x, peer 0x%x", ErrMissingCapabilities, required, c.peerCapabilities)
		}
		return c.peerCapabilities, nil
	}
	if err := c.write(protocol.Frame{Type: protocol.TypeHello, Payload: protocol.EncodeCapabilities(required)}); err != nil {
		return 0, err
	}
	select {
	case result := <-c.helloDone:
		if result.err != nil {
			return 0, result.err
		}
		c.peerCapabilities, c.handshakeComplete = result.capabilities, true
		if result.capabilities&required != required {
			return result.capabilities, fmt.Errorf("%w: required 0x%x, peer 0x%x", ErrMissingCapabilities, required, result.capabilities)
		}
		return result.capabilities, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-c.closed:
		return 0, ErrClientClosed
	}
}

func (c *Client) OpenPacketContext(ctx context.Context) (net.PacketConn, error) {
	id := c.nextID.Add(2)
	p := &clientPacketConn{client: c, id: id, ready: make(chan error, 1), incoming: make(chan packetEvent, 64), done: make(chan struct{})}
	c.mu.Lock()
	c.datagrams[id] = p
	c.mu.Unlock()
	if err := c.write(protocol.Frame{Type: protocol.TypeDatagramOpen, StreamID: id}); err != nil {
		c.removeDatagram(id)
		return nil, err
	}
	select {
	case err := <-p.ready:
		if err != nil {
			c.removeDatagram(id)
			return nil, err
		}
		return p, nil
	case <-ctx.Done():
		_ = p.Close()
		return nil, ctx.Err()
	case <-c.closed:
		return nil, ErrClientClosed
	}
}

// Run reads and dispatches frames until the transport closes or ctx is canceled.
func (c *Client) Run(ctx context.Context) error {
	started := false
	c.runOnce.Do(func() { started = true })
	if !started {
		return ErrClientAlreadyRunning
	}
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
		c.closeTransport()
		c.fail(ctx.Err())
		return ctx.Err()
	case <-c.closed:
		c.closeTransport()
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
	reservation, err := c.ReserveReverseForward(ctx, windowsAddr, target)
	if err != nil {
		return nil, err
	}
	if err := reservation.Commit(); err != nil {
		_ = reservation.Close()
		return nil, err
	}
	return reservation, nil
}

// ReserveReverseForward binds the Windows listener without accepting clients.
// Commit must be called after the corresponding WSL listener is ready.
func (c *Client) ReserveReverseForward(ctx context.Context, windowsAddr, target string) (*ReverseReservation, error) {
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
		return &ReverseReservation{listener: l}, nil
	case <-ctx.Done():
		_ = l.Close()
		return nil, ctx.Err()
	case <-c.closed:
		return nil, ErrClientClosed
	}
}

type ReverseReservation struct {
	listener   *clientListener
	commitOnce sync.Once
	commitErr  error
}

func (r *ReverseReservation) Commit() error {
	r.commitOnce.Do(func() {
		r.commitErr = r.listener.client.write(protocol.Frame{Type: protocol.TypeListenCommit, StreamID: r.listener.id})
	})
	return r.commitErr
}

func (r *ReverseReservation) Close() error { return r.listener.Close() }

func (c *Client) Close() error {
	c.fail(ErrClientClosed)
	c.closeTransport()
	return c.closeErr
}

func (c *Client) closeTransport() {
	c.transportCloseOne.Do(func() {
		if closer, ok := c.rw.(io.Closer); ok {
			_ = closer.Close()
		}
	})
}

func (c *Client) write(frame protocol.Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.Write(c.rw, frame)
}

func (c *Client) dispatch(frame protocol.Frame) {
	if frame.Type == protocol.TypeHelloOK {
		c.helloOnce.Do(func() { c.helloDone <- helloResult{capabilities: protocol.DecodeCapabilities(frame.Payload)} })
		return
	}
	if frame.Type == protocol.TypeDatagramOK || frame.Type == protocol.TypeDatagramError || frame.Type == protocol.TypeDatagramData || frame.Type == protocol.TypeDatagramClose {
		c.mu.Lock()
		packet := c.datagrams[frame.StreamID]
		c.mu.Unlock()
		if packet != nil {
			packet.handle(frame)
		}
		return
	}
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
			s := newClientStream(c, frame.StreamID, l.target)
			s.openOnce.Do(func() { s.openDone <- nil })
			c.mu.Lock()
			if _, exists := c.streams[frame.StreamID]; exists {
				c.mu.Unlock()
				_ = c.write(protocol.Frame{Type: protocol.TypeReset, StreamID: frame.StreamID, Payload: []byte("duplicate inbound stream id")})
				return
			}
			c.streams[frame.StreamID] = s
			c.mu.Unlock()
			go c.acceptInbound(l, s)
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
		c.helloOnce.Do(func() { c.helloDone <- helloResult{err: err} })
		c.mu.Lock()
		streams := make([]*clientStream, 0, len(c.streams))
		for _, s := range c.streams {
			streams = append(streams, s)
		}
		c.streams = make(map[uint32]*clientStream)
		c.listeners = make(map[uint32]*clientListener)
		datagrams := make([]*clientPacketConn, 0, len(c.datagrams))
		for _, packet := range c.datagrams {
			datagrams = append(datagrams, packet)
		}
		c.datagrams = make(map[uint32]*clientPacketConn)
		c.mu.Unlock()
		for _, s := range streams {
			s.fail(err)
		}
		for _, packet := range datagrams {
			packet.fail(err)
		}
	})
}

func (c *Client) removeDatagram(id uint32) { c.mu.Lock(); delete(c.datagrams, id); c.mu.Unlock() }

type packetEvent struct {
	endpoint string
	data     []byte
	err      error
}
type clientPacketConn struct {
	client        *Client
	id            uint32
	ready         chan error
	readyOnce     sync.Once
	incoming      chan packetEvent
	closeOnce     sync.Once
	done          chan struct{}
	doneOnce      sync.Once
	errorMu       sync.Mutex
	terminalErr   error
	deadlineMu    sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

func (p *clientPacketConn) handle(frame protocol.Frame) {
	switch frame.Type {
	case protocol.TypeDatagramOK:
		p.readyOnce.Do(func() { p.ready <- nil })
	case protocol.TypeDatagramError:
		err := errors.New(string(frame.Payload))
		p.readyOnce.Do(func() { p.ready <- err })
		p.fail(err)
	case protocol.TypeDatagramData:
		endpoint, data, err := protocol.DecodeDatagram(frame.Payload)
		if err != nil {
			p.fail(err)
			return
		}
		select {
		case p.incoming <- packetEvent{endpoint: endpoint, data: append([]byte(nil), data...)}:
		case <-p.client.closed:
		default:
			// UDP permits loss; never block every multiplexed flow for one slow association.
		}
	case protocol.TypeDatagramClose:
		p.fail(io.EOF)
	}
}

func (p *clientPacketConn) fail(err error) {
	p.readyOnce.Do(func() { p.ready <- err })
	p.errorMu.Lock()
	if p.terminalErr == nil {
		p.terminalErr = err
	}
	p.errorMu.Unlock()
	p.doneOnce.Do(func() { close(p.done) })
	select {
	case p.incoming <- packetEvent{err: err}:
	default:
	}
}

func (p *clientPacketConn) terminalError() error {
	p.errorMu.Lock()
	defer p.errorMu.Unlock()
	if p.terminalErr == nil {
		return io.EOF
	}
	return p.terminalErr
}

func (p *clientPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	p.deadlineMu.Lock()
	deadline := p.readDeadline
	p.deadlineMu.Unlock()
	var timer *time.Timer
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		duration := time.Until(deadline)
		if duration <= 0 {
			return 0, nil, osTimeout{}
		}
		timer = time.NewTimer(duration)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case event := <-p.incoming:
		if event.err != nil {
			return 0, nil, event.err
		}
		if len(event.data) > len(buffer) {
			copy(buffer, event.data[:len(buffer)])
			return len(buffer), relayAddr(event.endpoint), io.ErrShortBuffer
		}
		n := copy(buffer, event.data)
		return n, relayAddr(event.endpoint), nil
	case <-timeout:
		return 0, nil, osTimeout{}
	case <-p.done:
		return 0, nil, p.terminalError()
	case <-p.client.closed:
		return 0, nil, ErrClientClosed
	}
}

func (p *clientPacketConn) WriteTo(data []byte, address net.Addr) (int, error) {
	if address == nil {
		return 0, errors.New("datagram destination is required")
	}
	p.deadlineMu.Lock()
	deadline := p.writeDeadline
	p.deadlineMu.Unlock()
	if !deadline.IsZero() && time.Now().After(deadline) {
		return 0, osTimeout{}
	}
	payload, err := protocol.EncodeDatagram(address.String(), data)
	if err != nil {
		return 0, err
	}
	select {
	case <-p.done:
		return 0, p.terminalError()
	default:
	}
	if err := p.client.write(protocol.Frame{Type: protocol.TypeDatagramData, StreamID: p.id, Payload: payload}); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (p *clientPacketConn) Close() error {
	p.closeOnce.Do(func() {
		p.client.removeDatagram(p.id)
		p.fail(io.EOF)
		_ = p.client.write(protocol.Frame{Type: protocol.TypeDatagramClose, StreamID: p.id})
	})
	return nil
}
func (p *clientPacketConn) LocalAddr() net.Addr { return relayAddr("wsl-udp") }
func (p *clientPacketConn) SetDeadline(t time.Time) error {
	p.deadlineMu.Lock()
	p.readDeadline, p.writeDeadline = t, t
	p.deadlineMu.Unlock()
	return nil
}
func (p *clientPacketConn) SetReadDeadline(t time.Time) error {
	p.deadlineMu.Lock()
	p.readDeadline = t
	p.deadlineMu.Unlock()
	return nil
}
func (p *clientPacketConn) SetWriteDeadline(t time.Time) error {
	p.deadlineMu.Lock()
	p.writeDeadline = t
	p.deadlineMu.Unlock()
	return nil
}

var _ net.PacketConn = (*clientPacketConn)(nil)

func (c *Client) removeListener(id uint32) { c.mu.Lock(); delete(c.listeners, id); c.mu.Unlock() }

func (c *Client) acceptInbound(l *clientListener, s *clientStream) {
	local, err := (&net.Dialer{}).DialContext(l.ctx, "tcp", l.target)
	if err != nil {
		_ = c.write(protocol.Frame{Type: protocol.TypeReset, StreamID: s.id, Payload: []byte(err.Error())})
		c.removeStream(s.id)
		return
	}
	bridge(local, s)
	c.removeStream(s.id)
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
		l.client.removeListener(l.id)
		_ = l.client.write(protocol.Frame{Type: protocol.TypeListenClose, StreamID: l.id})
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
	readEOF       bool
	readBuf       []byte
	deadlineMu    sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
	errorMu       sync.Mutex
	terminalErr   error
	sendWindow    *flowWindow
	done          chan struct{}
	doneOnce      sync.Once
	halfCloseOnce sync.Once
}

type streamEvent struct {
	data []byte
	err  error
}

func newClientStream(c *Client, id uint32, target string) *clientStream {
	queueSize := protocol.InitialStreamWindow/protocol.MaxDataSize + 2
	return &clientStream{client: c, id: id, target: target, incoming: make(chan streamEvent, queueSize), openDone: make(chan error, 1), sendWindow: newFlowWindow(), done: make(chan struct{})}
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
		default:
			err := errors.New("stream receive window exceeded")
			_ = s.client.write(protocol.Frame{Type: protocol.TypeReset, StreamID: s.id, Payload: []byte(err.Error())})
			s.fail(err)
		}
	case protocol.TypeWindowUpdate:
		if !s.sendWindow.add(protocol.DecodeWindowUpdate(frame.Payload)) {
			s.fail(errors.New("invalid stream window update"))
		}
	case protocol.TypeHalfClose:
		s.stateMu.Lock()
		s.readEOF = true
		s.stateMu.Unlock()
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
	s.errorMu.Lock()
	if s.terminalErr == nil {
		s.terminalErr = err
	}
	s.errorMu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
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

func (s *clientStream) terminalError() error {
	s.errorMu.Lock()
	defer s.errorMu.Unlock()
	if s.terminalErr == nil {
		return io.EOF
	}
	return s.terminalErr
}

func (s *clientStream) Read(p []byte) (int, error) {
	for {
		if len(s.readBuf) > 0 {
			n := copy(p, s.readBuf)
			s.readBuf = s.readBuf[n:]
			if err := s.client.write(protocol.Frame{Type: protocol.TypeWindowUpdate, StreamID: s.id, Payload: protocol.EncodeWindowUpdate(uint32(n))}); err != nil {
				return n, err
			}
			return n, nil
		}
		s.stateMu.Lock()
		readEOF := s.readEOF
		s.stateMu.Unlock()
		if readEOF {
			select {
			case event := <-s.incoming:
				if len(event.data) > 0 {
					s.readBuf = event.data
					continue
				}
				if event.err != nil {
					return 0, event.err
				}
			default:
				return 0, io.EOF
			}
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
		case <-s.done:
			return 0, s.terminalError()
		case <-s.client.closed:
			return 0, ErrClientClosed
		}
	}
}

func (s *clientStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	for len(p) > 0 {
		s.deadlineMu.Lock()
		deadline := s.writeDeadline
		s.deadlineMu.Unlock()
		n := len(p)
		if n > protocol.MaxDataSize {
			n = protocol.MaxDataSize
		}
		n, err := s.sendWindow.take(n, s.done, deadline)
		if err != nil {
			return written, err
		}
		if err := s.client.write(protocol.Frame{Type: protocol.TypeData, StreamID: s.id, Payload: p[:n]}); err != nil {
			return written, err
		}
		p = p[n:]
		written += n
	}
	return written, nil
}

func (s *clientStream) Close() error {
	s.closeOnce.Do(func() {
		s.client.removeStream(s.id)
		s.fail(io.EOF)
		_ = s.client.write(protocol.Frame{Type: protocol.TypeClose, StreamID: s.id})
	})
	return nil
}

// CloseWrite half-closes the client-to-Windows direction while keeping reads open.
func (s *clientStream) CloseWrite() error {
	var err error
	s.halfCloseOnce.Do(func() { err = s.client.write(protocol.Frame{Type: protocol.TypeHalfClose, StreamID: s.id}) })
	return err
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
