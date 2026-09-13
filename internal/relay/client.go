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

	"github.com/Kevin589981/wsl-win-relay/internal/netutil"
	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/framed"
)

var ErrClientClosed = errors.New("relay client is closed")
var ErrClientAlreadyRunning = errors.New("relay client is already running")
var ErrMissingCapabilities = errors.New("Windows relay is missing required capabilities")
var ErrPeerRestarted = errors.New("relay peer broker restarted")

// Closing a logical relay object must not stall service shutdown while a
// reconnecting Link has no current attachment. Close frames are advisory once
// the peer is detached, so bound their best-effort delivery.
const closeFrameTimeout = 250 * time.Millisecond

type Client struct {
	rw                io.ReadWriter
	transport         frameTransport
	resolveUDP        func(context.Context, string, string) (*net.UDPAddr, error)
	writeMu           sync.Mutex
	mu                sync.Mutex
	streams           map[uint32]*clientStream
	listeners         map[uint32]*clientListener
	datagrams         map[uint32]*clientPacketConn
	reverseDatagrams  map[uint32]*clientReverseDatagram
	nextID            atomic.Uint32
	closed            chan struct{}
	closeOne          sync.Once
	transportCloseOne sync.Once
	closeErr          error
	runOnce           sync.Once
	helloDone         chan helloResult
	helloOnce         sync.Once
	handshakeMu       sync.Mutex
	helloMu           sync.Mutex
	peerCapabilities  uint64
	peerInstanceID    atomic.Uint64
	handshakeComplete bool
}

type helloResult struct {
	capabilities uint64
	instanceID   uint64
	err          error
}

func NewClient(rw io.ReadWriter) *Client {
	c := &Client{rw: rw, resolveUDP: netutil.ResolveUDPAddr, streams: make(map[uint32]*clientStream), listeners: make(map[uint32]*clientListener), datagrams: make(map[uint32]*clientPacketConn), reverseDatagrams: make(map[uint32]*clientReverseDatagram), closed: make(chan struct{}), helloDone: make(chan helloResult, 1)}
	c.nextID.Store(^uint32(0))
	return c
}

// NewClientWithLink creates a client whose frame transport can be replaced by
// a reconnecting connector. Use RunAttached and Rehandshake for replacement
// transports.
func NewClientWithLink(link *framed.Link) *Client {
	client := NewClient(nil)
	client.transport = link
	return client
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
	if err := c.writeContext(ctx, protocol.Frame{Type: protocol.TypeHello, Payload: protocol.EncodeCapabilities(required)}); err != nil {
		return 0, err
	}
	select {
	case result := <-c.helloChannel():
		if result.err != nil {
			return 0, result.err
		}
		c.peerCapabilities, c.handshakeComplete = result.capabilities, true
		c.peerInstanceID.Store(result.instanceID)
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

// Rehandshake starts a fresh capability exchange after a new Link attachment.
// Existing stream/listener registries remain intact; the peer's Hello response
// is routed through the same client dispatcher.
func (c *Client) Rehandshake(ctx context.Context, required uint64) (uint64, error) {
	c.handshakeMu.Lock()
	c.helloMu.Lock()
	c.handshakeComplete = false
	c.peerCapabilities = 0
	c.peerInstanceID.Store(0)
	c.helloDone = make(chan helloResult, 1)
	c.helloOnce = sync.Once{}
	c.helloMu.Unlock()
	c.handshakeMu.Unlock()
	return c.Handshake(ctx, required)
}

// PeerInstanceID returns the broker identity advertised by the most recent
// HELLO_OK. Legacy stdio peers return zero.
func (c *Client) PeerInstanceID() uint64 { return c.peerInstanceID.Load() }

// ResetPeerState invalidates state that belonged to a broker process which is
// no longer alive. The client itself remains usable; callers can re-register
// mappings on the new peer afterwards.
func (c *Client) ResetPeerState(cause error) {
	if cause == nil {
		cause = ErrPeerRestarted
	}
	c.mu.Lock()
	streams := make([]*clientStream, 0, len(c.streams))
	for _, stream := range c.streams {
		streams = append(streams, stream)
	}
	c.streams = make(map[uint32]*clientStream)
	listeners := make([]*clientListener, 0, len(c.listeners))
	for _, listener := range c.listeners {
		listeners = append(listeners, listener)
	}
	c.listeners = make(map[uint32]*clientListener)
	datagrams := make([]*clientPacketConn, 0, len(c.datagrams))
	for _, datagram := range c.datagrams {
		datagrams = append(datagrams, datagram)
	}
	c.datagrams = make(map[uint32]*clientPacketConn)
	reverseDatagrams := make([]*clientReverseDatagram, 0, len(c.reverseDatagrams))
	for _, datagram := range c.reverseDatagrams {
		reverseDatagrams = append(reverseDatagrams, datagram)
	}
	c.reverseDatagrams = make(map[uint32]*clientReverseDatagram)
	c.mu.Unlock()
	for _, stream := range streams {
		stream.fail(cause)
	}
	for _, listener := range listeners {
		listener.invalidate()
	}
	for _, datagram := range datagrams {
		datagram.fail(cause)
	}
	for _, datagram := range reverseDatagrams {
		datagram.fail(cause)
	}
}

func (c *Client) OpenPacketContext(ctx context.Context) (net.PacketConn, error) {
	id := c.nextID.Add(2)
	p := &clientPacketConn{client: c, id: id, ready: make(chan error, 1), incoming: make(chan packetEvent, datagramQueueDepth), done: make(chan struct{}), deadlineChanged: make(chan struct{})}
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return nil, ErrClientClosed
	default:
	}
	if len(c.datagrams) >= MaxConcurrentDatagrams {
		c.mu.Unlock()
		return nil, resourceLimitError("UDP associations", MaxConcurrentDatagrams)
	}
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
	if c.transport != nil {
		return errors.New("attached relay client requires RunAttached")
	}
	started := false
	c.runOnce.Do(func() { started = true })
	if !started {
		return ErrClientAlreadyRunning
	}
	result := make(chan error, 1)
	go func() {
		for {
			frame, err := c.readFrame()
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

// RunAttached keeps the client registry alive while its frame Link is
// detached. A subsequent attachment can rehandshake and continue using the
// same stream/listener objects.
func (c *Client) RunAttached(ctx context.Context) error {
	if c.transport == nil {
		return errors.New("attached relay client requires a frame transport")
	}
	started := false
	c.runOnce.Do(func() { started = true })
	if !started {
		return ErrClientAlreadyRunning
	}
	for {
		frame, err := c.readFrame()
		if err != nil {
			if ctx.Err() != nil {
				c.fail(ctx.Err())
				return ctx.Err()
			}
			if errors.Is(err, framed.ErrDetached) {
				continue
			}
			c.fail(err)
			return err
		}
		c.dispatch(frame)
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
	if len(c.streams) >= MaxConcurrentStreams {
		c.mu.Unlock()
		return nil, resourceLimitError("TCP streams", MaxConcurrentStreams)
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

// ReverseDatagramForward asks the Windows side to bind windowsAddr and
// forward received datagrams to the WSL-local UDP target.
func (c *Client) ReverseDatagramForward(ctx context.Context, windowsAddr, target string) (io.Closer, error) {
	if windowsAddr == "" || target == "" || len(windowsAddr) > protocol.MaxTargetSize || len(target) > protocol.MaxTargetSize {
		return nil, fmt.Errorf("invalid reverse datagram address")
	}
	targetAddr, err := netutil.ResolveUDPAddr(ctx, "udp", target)
	if err != nil {
		return nil, fmt.Errorf("resolve reverse datagram target: %w", err)
	}
	id := c.nextID.Add(2)
	listenerCtx, listenerCancel := context.WithCancel(context.Background())
	l := newClientReverseDatagram(c, id, windowsAddr, targetAddr, listenerCtx)
	l.cancel = listenerCancel
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		listenerCancel()
		return nil, ErrClientClosed
	default:
	}
	if len(c.reverseDatagrams) >= MaxConcurrentReverseDatagrams {
		c.mu.Unlock()
		listenerCancel()
		return nil, resourceLimitError("reverse UDP listeners", MaxConcurrentReverseDatagrams)
	}
	c.reverseDatagrams[id] = l
	c.mu.Unlock()
	if err := c.write(protocol.Frame{Type: protocol.TypeListenDatagramOpen, StreamID: id, Payload: []byte(windowsAddr)}); err != nil {
		listenerCancel()
		c.removeReverseDatagram(id)
		return nil, err
	}
	select {
	case result := <-l.ready:
		if result.err != nil {
			listenerCancel()
			c.removeReverseDatagram(id)
			return nil, result.err
		}
		l.boundAddress = result.address
		return l, nil
	case <-ctx.Done():
		_ = l.Close()
		return nil, ctx.Err()
	case <-c.closed:
		return nil, ErrClientClosed
	}
}

// ReserveReverseForward binds the Windows listener without accepting clients.
// Commit must be called after the corresponding WSL listener is ready.
func (c *Client) ReserveReverseForward(ctx context.Context, windowsAddr, target string) (*ReverseReservation, error) {
	if windowsAddr == "" || target == "" || len(windowsAddr) > protocol.MaxTargetSize || len(target) > protocol.MaxTargetSize {
		return nil, fmt.Errorf("invalid reverse-forward address")
	}
	id := c.nextID.Add(2)
	listenerCtx, listenerCancel := context.WithCancel(context.Background())
	l := &clientListener{id: id, client: c, requestedAddress: windowsAddr, target: target, ctx: listenerCtx, cancel: listenerCancel, ready: make(chan listenerReady, 1)}
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		listenerCancel()
		return nil, ErrClientClosed
	default:
	}
	if len(c.listeners) >= MaxConcurrentListeners {
		c.mu.Unlock()
		listenerCancel()
		return nil, resourceLimitError("TCP listeners", MaxConcurrentListeners)
	}
	c.listeners[id] = l
	c.mu.Unlock()
	if err := c.write(protocol.Frame{Type: protocol.TypeListenOpen, StreamID: id, Payload: []byte(windowsAddr)}); err != nil {
		listenerCancel()
		c.removeListener(id)
		return nil, err
	}
	select {
	case result := <-l.ready:
		if result.err != nil {
			listenerCancel()
			c.removeListener(id)
			return nil, result.err
		}
		l.boundAddress = result.address
		return &ReverseReservation{listener: l}, nil
	case <-ctx.Done():
		_ = l.Close()
		return nil, ctx.Err()
	case <-c.closed:
		listenerCancel()
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

// BoundAddress returns the address selected by Windows. It differs from the
// requested address when port zero asks Windows to allocate an ephemeral port.
func (r *ReverseReservation) BoundAddress() string { return r.listener.boundAddress }

func (c *Client) Close() error {
	c.fail(ErrClientClosed)
	c.closeTransport()
	return c.closeErr
}

func (c *Client) closeTransport() {
	c.transportCloseOne.Do(func() {
		if closer, ok := c.transport.(io.Closer); ok {
			_ = closer.Close()
			return
		}
		if closer, ok := c.rw.(io.Closer); ok {
			_ = closer.Close()
		}
	})
}

func (c *Client) write(frame protocol.Frame) error {
	return c.writeContext(context.Background(), frame)
}

func (c *Client) writeClose(frame protocol.Frame) {
	ctx, cancel := context.WithTimeout(context.Background(), closeFrameTimeout)
	defer cancel()
	_ = c.writeContext(ctx, frame)
}

func (c *Client) writeContext(ctx context.Context, frame protocol.Frame) error {
	if c.transport != nil {
		for {
			var err error
			if contextTransport, ok := c.transport.(interface {
				WriteFrameContext(context.Context, protocol.Frame) error
			}); ok {
				err = contextTransport.WriteFrameContext(ctx, frame)
			} else {
				err = c.transport.WriteFrame(frame)
			}
			if !errors.Is(err, framed.ErrDetached) {
				return err
			}
			select {
			case <-c.closed:
				return ErrClientClosed
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.Write(c.rw, frame)
}

func (c *Client) readFrame() (protocol.Frame, error) {
	if c.transport != nil {
		return c.transport.ReadFrame()
	}
	return protocol.Read(c.rw)
}

func (c *Client) helloChannel() <-chan helloResult {
	c.helloMu.Lock()
	defer c.helloMu.Unlock()
	return c.helloDone
}

func (c *Client) dispatch(frame protocol.Frame) {
	if frame.Type == protocol.TypeHelloOK {
		capabilities, instanceID, err := protocol.DecodeHelloOK(frame.Payload)
		c.helloMu.Lock()
		c.helloOnce.Do(func() { c.helloDone <- helloResult{capabilities: capabilities, instanceID: instanceID, err: err} })
		c.helloMu.Unlock()
		return
	}
	if frame.Type == protocol.TypeListenDatagramOK || frame.Type == protocol.TypeListenDatagramError || frame.Type == protocol.TypeListenDatagramData || frame.Type == protocol.TypeListenDatagramClose {
		c.mu.Lock()
		datagram := c.reverseDatagrams[frame.StreamID]
		c.mu.Unlock()
		if datagram != nil {
			datagram.handle(frame)
		}
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
				address := string(frame.Payload)
				if address == "" {
					address = l.requestedAddress
				}
				l.readyOnce.Do(func() { l.ready <- listenerReady{address: address} })
			} else {
				l.readyOnce.Do(func() { l.ready <- listenerReady{err: errors.New(string(frame.Payload))} })
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
		if l == nil {
			_ = c.write(protocol.Frame{Type: protocol.TypeReset, StreamID: frame.StreamID, Payload: []byte("unknown reverse listener")})
			return
		}
		s := newClientStream(c, frame.StreamID, l.target)
		s.openOnce.Do(func() { s.openDone <- nil })
		c.mu.Lock()
		if _, exists := c.streams[frame.StreamID]; exists {
			c.mu.Unlock()
			_ = c.write(protocol.Frame{Type: protocol.TypeReset, StreamID: frame.StreamID, Payload: []byte("duplicate inbound stream id")})
			return
		}
		if len(c.streams) >= MaxConcurrentStreams {
			c.mu.Unlock()
			_ = c.write(protocol.Frame{Type: protocol.TypeReset, StreamID: frame.StreamID, Payload: protocol.ErrorPayload(resourceLimitError("TCP streams", MaxConcurrentStreams))})
			return
		}
		c.streams[frame.StreamID] = s
		c.mu.Unlock()
		go c.acceptInbound(l, s)
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
		c.helloMu.Lock()
		c.helloOnce.Do(func() { c.helloDone <- helloResult{err: err} })
		c.helloMu.Unlock()
		c.mu.Lock()
		streams := make([]*clientStream, 0, len(c.streams))
		for _, s := range c.streams {
			streams = append(streams, s)
		}
		c.streams = make(map[uint32]*clientStream)
		listeners := make([]*clientListener, 0, len(c.listeners))
		for _, listener := range c.listeners {
			listeners = append(listeners, listener)
		}
		c.listeners = make(map[uint32]*clientListener)
		datagrams := make([]*clientPacketConn, 0, len(c.datagrams))
		for _, packet := range c.datagrams {
			datagrams = append(datagrams, packet)
		}
		c.datagrams = make(map[uint32]*clientPacketConn)
		reverseDatagrams := make([]*clientReverseDatagram, 0, len(c.reverseDatagrams))
		for _, datagram := range c.reverseDatagrams {
			reverseDatagrams = append(reverseDatagrams, datagram)
		}
		c.reverseDatagrams = make(map[uint32]*clientReverseDatagram)
		c.mu.Unlock()
		for _, s := range streams {
			s.fail(err)
		}
		for _, listener := range listeners {
			listener.invalidate()
		}
		for _, packet := range datagrams {
			packet.fail(err)
		}
		for _, datagram := range reverseDatagrams {
			datagram.fail(err)
		}
	})
}

func (c *Client) removeDatagram(id uint32) { c.mu.Lock(); delete(c.datagrams, id); c.mu.Unlock() }
func (c *Client) removeReverseDatagram(id uint32) {
	c.mu.Lock()
	delete(c.reverseDatagrams, id)
	c.mu.Unlock()
}

type packetEvent struct {
	endpoint string
	data     []byte
	err      error
}
type clientPacketConn struct {
	client          *Client
	id              uint32
	ready           chan error
	readyOnce       sync.Once
	incoming        chan packetEvent
	closeOnce       sync.Once
	done            chan struct{}
	doneOnce        sync.Once
	errorMu         sync.Mutex
	terminalErr     error
	deadlineMu      sync.Mutex
	readDeadline    time.Time
	writeDeadline   time.Time
	deadlineChanged chan struct{}
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
	for {
		p.deadlineMu.Lock()
		deadline := p.readDeadline
		deadlineChanged := p.deadlineChanged
		if deadlineChanged == nil {
			deadlineChanged = make(chan struct{})
			p.deadlineChanged = deadlineChanged
		}
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
		}
		select {
		case event := <-p.incoming:
			if timer != nil {
				timer.Stop()
			}
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
		case <-deadlineChanged:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-p.done:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, p.terminalError()
		case <-p.client.closed:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, ErrClientClosed
		}
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
		p.client.writeClose(protocol.Frame{Type: protocol.TypeDatagramClose, StreamID: p.id})
	})
	return nil
}
func (p *clientPacketConn) LocalAddr() net.Addr { return relayAddr("wsl-udp") }
func (p *clientPacketConn) SetDeadline(t time.Time) error {
	p.deadlineMu.Lock()
	p.readDeadline, p.writeDeadline = t, t
	changed := p.deadlineChanged
	p.deadlineChanged = make(chan struct{})
	p.deadlineMu.Unlock()
	if changed != nil {
		close(changed)
	}
	return nil
}
func (p *clientPacketConn) SetReadDeadline(t time.Time) error {
	p.deadlineMu.Lock()
	p.readDeadline = t
	changed := p.deadlineChanged
	p.deadlineChanged = make(chan struct{})
	p.deadlineMu.Unlock()
	if changed != nil {
		close(changed)
	}
	return nil
}
func (p *clientPacketConn) SetWriteDeadline(t time.Time) error {
	p.deadlineMu.Lock()
	p.writeDeadline = t
	changed := p.deadlineChanged
	p.deadlineChanged = make(chan struct{})
	p.deadlineMu.Unlock()
	if changed != nil {
		close(changed)
	}
	return nil
}

var _ net.PacketConn = (*clientPacketConn)(nil)

func (c *Client) removeListener(id uint32) { c.mu.Lock(); delete(c.listeners, id); c.mu.Unlock() }

func (c *Client) acceptInbound(l *clientListener, s *clientStream) {
	local, err := (&net.Dialer{}).DialContext(l.ctx, "tcp", l.target)
	if err != nil {
		_ = c.write(protocol.Frame{Type: protocol.TypeReset, StreamID: s.id, Payload: protocol.ErrorPayload(err)})
		c.removeStream(s.id)
		return
	}
	bridge(local, s)
	c.removeStream(s.id)
}

// Expired per-source sockets are reclaimed by the idle deadline.
const reverseDatagramIdleTimeout = 5 * time.Minute

type clientReverseDatagram struct {
	client           *Client
	id               uint32
	requestedAddress string
	boundAddress     string
	target           *net.UDPAddr
	ctx              context.Context
	cancel           context.CancelFunc
	ready            chan listenerReady
	readyOnce        sync.Once
	incoming         chan []byte
	done             chan struct{}
	closeOnce        sync.Once
	mu               sync.Mutex
	flows            map[string]*reverseDatagramFlow
}

type reverseDatagramFlow struct {
	listener  *clientReverseDatagram
	key       string
	remote    string
	conn      *net.UDPConn
	closeOnce sync.Once
}

func newClientReverseDatagram(client *Client, id uint32, requestedAddress string, target *net.UDPAddr, ctx context.Context) *clientReverseDatagram {
	listener := &clientReverseDatagram{client: client, id: id, requestedAddress: requestedAddress, target: target, ctx: ctx, ready: make(chan listenerReady, 1), incoming: make(chan []byte, datagramQueueDepth), done: make(chan struct{}), flows: make(map[string]*reverseDatagramFlow)}
	go listener.writeDatagrams()
	return listener
}

func (l *clientReverseDatagram) handle(frame protocol.Frame) {
	switch frame.Type {
	case protocol.TypeListenDatagramOK:
		address := string(frame.Payload)
		if address == "" {
			address = l.requestedAddress
		}
		l.readyOnce.Do(func() { l.ready <- listenerReady{address: address} })
	case protocol.TypeListenDatagramError:
		err := errors.New(string(frame.Payload))
		l.readyOnce.Do(func() { l.ready <- listenerReady{err: err} })
		l.fail(err)
	case protocol.TypeListenDatagramClose:
		l.fail(io.EOF)
	case protocol.TypeListenDatagramData:
		select {
		case l.incoming <- append([]byte(nil), frame.Payload...):
		case <-l.done:
		case <-l.ctx.Done():
		default:
		}
	}
}

func (l *clientReverseDatagram) writeDatagrams() {
	for {
		select {
		case payload := <-l.incoming:
			select {
			case <-l.done:
				return
			case <-l.ctx.Done():
				return
			default:
			}
			l.writeDatagram(payload)
		case <-l.done:
			return
		case <-l.ctx.Done():
			return
		}
	}
}

func (l *clientReverseDatagram) writeDatagram(payload []byte) {
	endpoint, data, err := protocol.DecodeDatagram(payload)
	if err != nil {
		l.fail(err)
		return
	}
	remote, err := l.client.resolveUDP(l.ctx, "udp", endpoint)
	if err != nil {
		l.fail(err)
		return
	}
	key := remote.String()
	l.mu.Lock()
	select {
	case <-l.done:
		l.mu.Unlock()
		return
	default:
	}
	flow := l.flows[key]
	if flow == nil {
		if len(l.flows) >= MaxReverseDatagramFlows {
			l.mu.Unlock()
			return
		}
		conn, dialErr := net.DialUDP("udp", nil, l.target)
		if dialErr != nil {
			l.mu.Unlock()
			l.fail(dialErr)
			return
		}
		flow = &reverseDatagramFlow{listener: l, key: key, remote: endpoint, conn: conn}
		l.flows[key] = flow
		go flow.readLoop()
	}
	l.mu.Unlock()
	_ = flow.conn.SetReadDeadline(time.Now().Add(reverseDatagramIdleTimeout))
	if _, err := flow.conn.Write(data); err != nil {
		l.removeFlow(flow)
	}
}

func (l *clientReverseDatagram) fail(err error) {
	l.readyOnce.Do(func() { l.ready <- listenerReady{err: err} })
	l.closeOnce.Do(func() {
		close(l.done)
		if l.cancel != nil {
			l.cancel()
		}
		l.client.removeReverseDatagram(l.id)
		l.mu.Lock()
		flows := make([]*reverseDatagramFlow, 0, len(l.flows))
		for _, flow := range l.flows {
			flows = append(flows, flow)
		}
		l.flows = make(map[string]*reverseDatagramFlow)
		l.mu.Unlock()
		for _, flow := range flows {
			flow.close()
		}
	})
}

func (l *clientReverseDatagram) BoundAddress() string { return l.boundAddress }

func (l *clientReverseDatagram) removeFlow(flow *reverseDatagramFlow) {
	l.mu.Lock()
	if current := l.flows[flow.key]; current == flow {
		delete(l.flows, flow.key)
	}
	l.mu.Unlock()
	flow.close()
}

func (l *clientReverseDatagram) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
		if l.cancel != nil {
			l.cancel()
		}
		l.client.removeReverseDatagram(l.id)
		l.mu.Lock()
		flows := make([]*reverseDatagramFlow, 0, len(l.flows))
		for _, flow := range l.flows {
			flows = append(flows, flow)
		}
		l.flows = make(map[string]*reverseDatagramFlow)
		l.mu.Unlock()
		for _, flow := range flows {
			flow.close()
		}
		l.client.writeClose(protocol.Frame{Type: protocol.TypeListenDatagramClose, StreamID: l.id})
	})
	return nil
}

func (f *reverseDatagramFlow) readLoop() {
	buffer := make([]byte, 65535)
	for {
		count, err := f.conn.Read(buffer)
		if err != nil {
			f.listener.removeFlow(f)
			return
		}
		payload, err := protocol.EncodeDatagram(f.remote, buffer[:count])
		if err != nil {
			f.listener.removeFlow(f)
			return
		}
		if err := f.listener.client.write(protocol.Frame{Type: protocol.TypeListenDatagramData, StreamID: f.listener.id, Payload: payload}); err != nil {
			f.listener.removeFlow(f)
			return
		}
	}
}

func (f *reverseDatagramFlow) close() {
	f.closeOnce.Do(func() { _ = f.conn.Close() })
}

type clientListener struct {
	id               uint32
	client           *Client
	requestedAddress string
	boundAddress     string
	target           string
	ctx              context.Context
	cancel           context.CancelFunc
	ready            chan listenerReady
	readyOnce        sync.Once
	once             sync.Once
}

type listenerReady struct {
	address string
	err     error
}

func (l *clientListener) invalidate() {
	l.once.Do(func() {
		l.cancel()
		l.client.removeListener(l.id)
	})
}

func (l *clientListener) Close() error {
	l.once.Do(func() {
		l.cancel()
		l.client.removeListener(l.id)
		l.client.writeClose(protocol.Frame{Type: protocol.TypeListenClose, StreamID: l.id})
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
	client          *Client
	id              uint32
	target          string
	incoming        chan streamEvent
	openDone        chan error
	openOnce        sync.Once
	closeOnce       sync.Once
	stateMu         sync.Mutex
	closedRemote    bool
	readEOF         bool
	readBuf         []byte
	readMu          sync.Mutex
	readWake        chan struct{}
	deadlineMu      sync.Mutex
	readDeadline    time.Time
	writeDeadline   time.Time
	deadlineChanged chan struct{}
	errorMu         sync.Mutex
	terminalErr     error
	sendWindow      *flowWindow
	done            chan struct{}
	doneOnce        sync.Once
	halfCloseOnce   sync.Once
}

type streamEvent struct {
	data []byte
	err  error
}

func newClientStream(c *Client, id uint32, target string) *clientStream {
	queueSize := protocol.InitialStreamWindow/protocol.MaxDataSize + 2
	return &clientStream{client: c, id: id, target: target, incoming: make(chan streamEvent, queueSize), openDone: make(chan error, 1), sendWindow: newFlowWindow(), done: make(chan struct{}), readWake: make(chan struct{}), deadlineChanged: make(chan struct{})}
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
			_ = s.client.write(protocol.Frame{Type: protocol.TypeReset, StreamID: s.id, Payload: protocol.ErrorPayload(err)})
			s.fail(err)
		}
	case protocol.TypeWindowUpdate:
		if !s.sendWindow.add(protocol.DecodeWindowUpdate(frame.Payload)) {
			s.fail(errors.New("invalid stream window update"))
		}
	case protocol.TypeHalfClose:
		s.stateMu.Lock()
		s.readEOF = true
		wake := s.readWake
		s.readWake = make(chan struct{})
		s.stateMu.Unlock()
		if wake != nil {
			close(wake)
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
	if len(p) == 0 {
		return 0, nil
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
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
		readWake := s.readWake
		if readWake == nil {
			readWake = make(chan struct{})
			s.readWake = readWake
		}
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
		deadlineChanged := s.deadlineChanged
		if deadlineChanged == nil {
			deadlineChanged = make(chan struct{})
			s.deadlineChanged = deadlineChanged
		}
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
				if errors.Is(event.err, io.EOF) {
					s.stateMu.Lock()
					s.readEOF = true
					s.stateMu.Unlock()
				}
				return 0, event.err
			}
		case <-timer:
			return 0, osTimeout{}
		case <-deadlineChanged:
			continue
		case <-readWake:
			continue
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
		deadlineChanged := s.deadlineChanged
		if deadlineChanged == nil {
			deadlineChanged = make(chan struct{})
			s.deadlineChanged = deadlineChanged
		}
		s.deadlineMu.Unlock()
		n := len(p)
		if n > protocol.MaxDataSize {
			n = protocol.MaxDataSize
		}
		n, err := s.sendWindow.take(n, s.done, deadlineChanged, deadline)
		if errors.Is(err, errDeadlineChanged) {
			continue
		}
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
		s.client.writeClose(protocol.Frame{Type: protocol.TypeClose, StreamID: s.id})
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
	changed := s.deadlineChanged
	s.deadlineChanged = make(chan struct{})
	s.deadlineMu.Unlock()
	if changed != nil {
		close(changed)
	}
	return nil
}
func (s *clientStream) SetReadDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.readDeadline = t
	changed := s.deadlineChanged
	s.deadlineChanged = make(chan struct{})
	s.deadlineMu.Unlock()
	if changed != nil {
		close(changed)
	}
	return nil
}
func (s *clientStream) SetWriteDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.writeDeadline = t
	changed := s.deadlineChanged
	s.deadlineChanged = make(chan struct{})
	s.deadlineMu.Unlock()
	if changed != nil {
		close(changed)
	}
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
