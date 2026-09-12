package relay

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/framed"
)

type DialContextFunc func(context.Context, string) (net.Conn, error)
type PacketDialContextFunc func(context.Context) (net.PacketConn, error)
type targetPacketWriter interface {
	WriteToTarget([]byte, string) (int, error)
}

type Server struct {
	rw               io.ReadWriter
	transport        frameTransport
	dial             DialContextFunc
	packetDial       PacketDialContextFunc
	writeMu          sync.Mutex
	mu               sync.Mutex
	streams          map[uint32]*serverStream
	listeners        map[uint32]*serverListener
	datagrams        map[uint32]*serverDatagram
	reverseDatagrams map[uint32]*serverReverseDatagram
	nextStream       atomic.Uint32
	ctx              context.Context
	cancel           context.CancelFunc
	serveOnce        sync.Once
}

type frameTransport interface {
	ReadFrame() (protocol.Frame, error)
	WriteFrame(protocol.Frame) error
}

var ErrServerAlreadyRunning = errors.New("relay server is already running")

type serverStream struct {
	conn       net.Conn
	cancel     context.CancelFunc
	incoming   chan serverStreamEvent
	sendWindow *flowWindow
	done       chan struct{}
	closeOnce  sync.Once
}

type serverStreamEvent struct {
	data      []byte
	halfClose bool
}

func newServerStream(conn net.Conn, cancel context.CancelFunc) *serverStream {
	queueSize := protocol.InitialStreamWindow/protocol.MaxDataSize + 2
	return &serverStream{conn: conn, cancel: cancel, incoming: make(chan serverStreamEvent, queueSize), sendWindow: newFlowWindow(), done: make(chan struct{})}
}

func NewServer(rw io.ReadWriter, dial DialContextFunc) *Server {
	if dial == nil {
		d := &net.Dialer{}
		dial = func(ctx context.Context, target string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", target)
		}
	}
	packetDial := func(context.Context) (net.PacketConn, error) { return net.ListenUDP("udp", nil) }
	return NewServerWithPacketDialer(rw, dial, packetDial)
}

func NewServerWithPacketDialer(rw io.ReadWriter, dial DialContextFunc, packetDial PacketDialContextFunc) *Server {
	if dial == nil {
		d := &net.Dialer{}
		dial = func(ctx context.Context, target string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", target)
		}
	}
	if packetDial == nil {
		packetDial = func(context.Context) (net.PacketConn, error) { return net.ListenUDP("udp", nil) }
	}
	return &Server{rw: rw, dial: dial, packetDial: packetDial, streams: make(map[uint32]*serverStream), listeners: make(map[uint32]*serverListener), datagrams: make(map[uint32]*serverDatagram), reverseDatagrams: make(map[uint32]*serverReverseDatagram)}
}

// NewServerWithLink creates a server whose frame transport can be replaced by
// a reconnecting connector. Use ServeAttached instead of Serve.
func NewServerWithLink(link *framed.Link, dial DialContextFunc, packetDial PacketDialContextFunc) *Server {
	server := NewServerWithPacketDialer(nil, dial, packetDial)
	server.transport = link
	return server
}

func (s *Server) Serve(ctx context.Context) error {
	if s.transport != nil {
		return errors.New("attached relay server requires ServeAttached")
	}
	started := false
	s.serveOnce.Do(func() { started = true })
	if !started {
		return ErrServerAlreadyRunning
	}
	serveCtx, cancel := context.WithCancel(ctx)
	s.ctx, s.cancel = serveCtx, cancel
	defer s.shutdown()
	transportDone := make(chan struct{})
	defer close(transportDone)
	go func() {
		select {
		case <-serveCtx.Done():
			if closer, ok := s.rw.(io.Closer); ok {
				_ = closer.Close()
			}
		case <-transportDone:
		}
	}()
	for {
		frame, err := protocol.Read(s.rw)
		if err != nil {
			if serveCtx.Err() != nil {
				return serveCtx.Err()
			}
			return err
		}
		s.handle(frame)
	}
}

// ServeAttached keeps broker-owned streams and listeners alive while a frame
// transport is detached. The next attachment can resume frame processing;
// only context cancellation or a non-recoverable protocol error shuts down
// the remote sockets.
func (s *Server) ServeAttached(ctx context.Context) error {
	if s.transport == nil {
		return errors.New("attached relay server requires a frame transport")
	}
	started := false
	s.serveOnce.Do(func() { started = true })
	if !started {
		return ErrServerAlreadyRunning
	}
	serveCtx, cancel := context.WithCancel(ctx)
	s.ctx, s.cancel = serveCtx, cancel
	defer s.shutdown()
	transportDone := make(chan struct{})
	defer close(transportDone)
	go func() {
		select {
		case <-serveCtx.Done():
			if closer, ok := s.transport.(io.Closer); ok {
				_ = closer.Close()
			}
		case <-transportDone:
		}
	}()
	for {
		frame, err := s.transport.ReadFrame()
		if err != nil {
			if serveCtx.Err() != nil {
				return serveCtx.Err()
			}
			if errors.Is(err, framed.ErrDetached) {
				continue
			}
			return err
		}
		s.handle(frame)
	}
}

func (s *Server) handle(frame protocol.Frame) {
	switch frame.Type {
	case protocol.TypeHello:
		_ = s.send(protocol.Frame{Type: protocol.TypeHelloOK, Payload: protocol.EncodeCapabilities(protocol.AllCapabilities)})
	case protocol.TypeOpen:
		go s.open(frame.StreamID, string(frame.Payload))
	case protocol.TypeListenOpen:
		go s.openListener(frame.StreamID, string(frame.Payload))
	case protocol.TypeListenClose:
		s.removeListener(frame.StreamID)
	case protocol.TypeListenCommit:
		s.commitListener(frame.StreamID)
	case protocol.TypeListenDatagramOpen:
		// Bind before processing the next control frame. This preserves OPEN/CLOSE
		// ordering so a client that cancels immediately cannot leave a late socket.
		s.openReverseDatagram(frame.StreamID, string(frame.Payload))
	case protocol.TypeListenDatagramData:
		s.writeReverseDatagram(frame.StreamID, frame.Payload)
	case protocol.TypeListenDatagramClose:
		s.removeReverseDatagram(frame.StreamID)
	case protocol.TypeDatagramOpen:
		go s.openDatagram(frame.StreamID)
	case protocol.TypeDatagramData:
		s.writeDatagram(frame.StreamID, frame.Payload)
	case protocol.TypeDatagramClose:
		s.removeDatagram(frame.StreamID)
	case protocol.TypeData:
		s.mu.Lock()
		stream := s.streams[frame.StreamID]
		s.mu.Unlock()
		if stream != nil {
			select {
			case stream.incoming <- serverStreamEvent{data: append([]byte(nil), frame.Payload...)}:
			case <-stream.done:
			default:
				s.reset(frame.StreamID, errors.New("stream receive window exceeded"))
			}
		}
	case protocol.TypeHalfClose:
		s.mu.Lock()
		stream := s.streams[frame.StreamID]
		s.mu.Unlock()
		if stream != nil {
			select {
			case stream.incoming <- serverStreamEvent{halfClose: true}:
			case <-stream.done:
			}
		}
	case protocol.TypeWindowUpdate:
		s.mu.Lock()
		stream := s.streams[frame.StreamID]
		s.mu.Unlock()
		if stream != nil && !stream.sendWindow.add(protocol.DecodeWindowUpdate(frame.Payload)) {
			s.reset(frame.StreamID, errors.New("invalid stream window update"))
		}
	case protocol.TypeClose, protocol.TypeReset:
		s.remove(frame.StreamID)
	}
}

type serverDatagram struct {
	conn      net.PacketConn
	incoming  chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (s *Server) openDatagram(id uint32) {
	conn, err := s.packetDial(s.ctx)
	if err != nil {
		_ = s.send(protocol.Frame{Type: protocol.TypeDatagramError, StreamID: id, Payload: []byte(err.Error())})
		return
	}
	s.mu.Lock()
	if _, exists := s.datagrams[id]; exists {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	datagram := &serverDatagram{conn: conn, incoming: make(chan []byte, 64), done: make(chan struct{})}
	s.datagrams[id] = datagram
	s.mu.Unlock()
	if err := s.send(protocol.Frame{Type: protocol.TypeDatagramOK, StreamID: id}); err != nil {
		s.removeDatagram(id)
		return
	}
	go s.writeDatagrams(id, datagram)
	go s.readDatagrams(id, conn)
}

func (s *Server) writeDatagram(id uint32, payload []byte) {
	s.mu.Lock()
	datagram := s.datagrams[id]
	s.mu.Unlock()
	if datagram == nil {
		return
	}
	select {
	case datagram.incoming <- append([]byte(nil), payload...):
	case <-datagram.done:
	default:
		// Datagram loss is preferable to blocking unrelated multiplexed flows.
	}
}

func (s *Server) writeDatagrams(id uint32, datagram *serverDatagram) {
	for {
		select {
		case payload := <-datagram.incoming:
			target, data, err := protocol.DecodeDatagram(payload)
			if err != nil {
				s.datagramError(id, err)
				continue
			}
			var writeErr error
			if targetWriter, ok := datagram.conn.(targetPacketWriter); ok {
				_, writeErr = targetWriter.WriteToTarget(data, target)
			} else {
				address, resolveErr := net.ResolveUDPAddr("udp", target)
				if resolveErr != nil {
					s.datagramError(id, resolveErr)
					continue
				}
				_, writeErr = datagram.conn.WriteTo(data, address)
			}
			if writeErr != nil {
				s.datagramError(id, writeErr)
			}
		case <-datagram.done:
			return
		}
	}
}

func (s *Server) readDatagrams(id uint32, conn net.PacketConn) {
	buffer := make([]byte, 65535)
	for {
		count, source, err := conn.ReadFrom(buffer)
		if err != nil {
			s.removeDatagram(id)
			return
		}
		payload, err := protocol.EncodeDatagram(source.String(), buffer[:count])
		if err != nil {
			continue
		}
		if err := s.send(protocol.Frame{Type: protocol.TypeDatagramData, StreamID: id, Payload: payload}); err != nil {
			s.removeDatagram(id)
			return
		}
	}
}

func (s *Server) datagramError(id uint32, err error) {
	_ = s.send(protocol.Frame{Type: protocol.TypeDatagramError, StreamID: id, Payload: []byte(err.Error())})
}

func (s *Server) reverseDatagramError(id uint32, err error) {
	_ = s.send(protocol.Frame{Type: protocol.TypeListenDatagramError, StreamID: id, Payload: []byte(err.Error())})
}
func (s *Server) removeDatagram(id uint32) {
	s.mu.Lock()
	datagram := s.datagrams[id]
	delete(s.datagrams, id)
	s.mu.Unlock()
	if datagram != nil {
		datagram.closeOnce.Do(func() { close(datagram.done); _ = datagram.conn.Close() })
	}
}

type serverReverseDatagram struct {
	conn      *net.UDPConn
	incoming  chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (s *Server) openReverseDatagram(id uint32, addr string) {
	if id == 0 || addr == "" {
		_ = s.send(protocol.Frame{Type: protocol.TypeListenDatagramError, StreamID: id, Payload: []byte("invalid datagram listen request")})
		return
	}
	address, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		_ = s.send(protocol.Frame{Type: protocol.TypeListenDatagramError, StreamID: id, Payload: []byte(err.Error())})
		return
	}
	conn, err := net.ListenUDP("udp", address)
	if err != nil {
		_ = s.send(protocol.Frame{Type: protocol.TypeListenDatagramError, StreamID: id, Payload: []byte(err.Error())})
		return
	}
	s.mu.Lock()
	if _, exists := s.reverseDatagrams[id]; exists {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	datagram := &serverReverseDatagram{conn: conn, incoming: make(chan []byte, 64), done: make(chan struct{})}
	s.reverseDatagrams[id] = datagram
	s.mu.Unlock()
	if err := s.send(protocol.Frame{Type: protocol.TypeListenDatagramOK, StreamID: id}); err != nil {
		s.removeReverseDatagram(id)
		return
	}
	go s.writeReverseDatagrams(id, datagram)
	go s.readReverseDatagrams(id, datagram)
}

func (s *Server) writeReverseDatagram(id uint32, payload []byte) {
	s.mu.Lock()
	datagram := s.reverseDatagrams[id]
	s.mu.Unlock()
	if datagram == nil {
		return
	}
	select {
	case datagram.incoming <- append([]byte(nil), payload...):
	case <-datagram.done:
	default:
	}
}

func (s *Server) writeReverseDatagrams(id uint32, datagram *serverReverseDatagram) {
	for {
		select {
		case payload := <-datagram.incoming:
			target, data, err := protocol.DecodeDatagram(payload)
			if err != nil {
				s.reverseDatagramError(id, err)
				continue
			}
			address, err := net.ResolveUDPAddr("udp", target)
			if err != nil {
				s.reverseDatagramError(id, err)
				continue
			}
			if _, err := datagram.conn.WriteToUDP(data, address); err != nil {
				s.reverseDatagramError(id, err)
			}
		case <-datagram.done:
			return
		}
	}
}

func (s *Server) readReverseDatagrams(id uint32, datagram *serverReverseDatagram) {
	buffer := make([]byte, 65535)
	for {
		count, source, err := datagram.conn.ReadFromUDP(buffer)
		if err != nil {
			s.removeReverseDatagram(id)
			_ = s.send(protocol.Frame{Type: protocol.TypeListenDatagramClose, StreamID: id})
			return
		}
		payload, err := protocol.EncodeDatagram(source.String(), buffer[:count])
		if err != nil {
			continue
		}
		if err := s.send(protocol.Frame{Type: protocol.TypeListenDatagramData, StreamID: id, Payload: payload}); err != nil {
			s.removeReverseDatagram(id)
			return
		}
	}
}

func (s *Server) removeReverseDatagram(id uint32) {
	s.mu.Lock()
	datagram := s.reverseDatagrams[id]
	delete(s.reverseDatagrams, id)
	s.mu.Unlock()
	if datagram != nil {
		datagram.closeOnce.Do(func() { close(datagram.done); _ = datagram.conn.Close() })
	}
}

type serverListener struct {
	listener   net.Listener
	cancel     context.CancelFunc
	commit     chan struct{}
	commitOnce sync.Once
}

func (s *Server) openListener(id uint32, addr string) {
	if id == 0 || addr == "" {
		_ = s.send(protocol.Frame{Type: protocol.TypeListenError, StreamID: id, Payload: []byte("invalid listen request")})
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = s.send(protocol.Frame{Type: protocol.TypeListenError, StreamID: id, Payload: []byte(err.Error())})
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	listener := &serverListener{listener: ln, cancel: cancel, commit: make(chan struct{})}
	s.mu.Lock()
	if _, exists := s.listeners[id]; exists {
		s.mu.Unlock()
		cancel()
		_ = ln.Close()
		return
	}
	s.listeners[id] = listener
	s.mu.Unlock()
	if err := s.send(protocol.Frame{Type: protocol.TypeListenOK, StreamID: id}); err != nil {
		s.removeListener(id)
		return
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	select {
	case <-listener.commit:
	case <-ctx.Done():
		return
	}
	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			s.removeListener(id)
			_ = s.send(protocol.Frame{Type: protocol.TypeListenClose, StreamID: id})
			return
		}
		streamID := s.nextStream.Add(2)
		stream := newServerStream(conn, func() {})
		s.mu.Lock()
		s.streams[streamID] = stream
		s.mu.Unlock()
		go s.writeToRemote(streamID, stream)
		payload := make([]byte, 4)
		binary.BigEndian.PutUint32(payload, id)
		if err := s.send(protocol.Frame{Type: protocol.TypeInboundOpen, StreamID: streamID, Payload: payload}); err != nil {
			s.remove(streamID)
			return
		}
		go s.copyToClient(streamID, conn)
	}
}

func (s *Server) commitListener(id uint32) {
	s.mu.Lock()
	l := s.listeners[id]
	s.mu.Unlock()
	if l != nil {
		l.commitOnce.Do(func() { close(l.commit) })
	}
}

func (s *Server) removeListener(id uint32) {
	s.mu.Lock()
	l := s.listeners[id]
	delete(s.listeners, id)
	s.mu.Unlock()
	if l != nil {
		l.cancel()
		_ = l.listener.Close()
	}
}

func (s *Server) open(id uint32, target string) {
	if id == 0 || target == "" {
		s.send(protocol.Frame{Type: protocol.TypeOpenError, StreamID: id, Payload: []byte("invalid open request")})
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	stream := newServerStream(nil, cancel)
	s.mu.Lock()
	if _, exists := s.streams[id]; exists {
		s.mu.Unlock()
		cancel()
		return
	}
	s.streams[id] = stream
	s.mu.Unlock()
	conn, err := s.dial(ctx, target)
	if err != nil {
		s.remove(id)
		s.send(protocol.Frame{Type: protocol.TypeOpenError, StreamID: id, Payload: []byte(err.Error())})
		return
	}
	s.mu.Lock()
	current := s.streams[id]
	if current == nil {
		s.mu.Unlock()
		conn.Close()
		return
	}
	current.conn = conn
	s.mu.Unlock()
	go s.writeToRemote(id, current)
	if err := s.send(protocol.Frame{Type: protocol.TypeOpenOK, StreamID: id}); err != nil {
		s.remove(id)
		return
	}
	go s.copyToClient(id, conn)
}

func (s *Server) copyToClient(id uint32, conn net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			s.mu.Lock()
			stream := s.streams[id]
			s.mu.Unlock()
			if stream == nil {
				return
			}
			if _, creditErr := stream.sendWindow.take(n, stream.done, nil, time.Time{}); creditErr != nil {
				return
			}
			if sendErr := s.send(protocol.Frame{Type: protocol.TypeData, StreamID: id, Payload: append([]byte(nil), buf[:n]...)}); sendErr != nil {
				s.remove(id)
				return
			}
		}
		if err != nil {
			s.send(protocol.Frame{Type: protocol.TypeHalfClose, StreamID: id})
			return
		}
	}
}

func (s *Server) writeToRemote(id uint32, stream *serverStream) {
	for {
		select {
		case event := <-stream.incoming:
			if event.halfClose {
				if cw, ok := stream.conn.(interface{ CloseWrite() error }); ok {
					if err := cw.CloseWrite(); err != nil {
						s.reset(id, err)
						return
					}
				}
				continue
			}
			written := 0
			for written < len(event.data) {
				count, err := stream.conn.Write(event.data[written:])
				if count > 0 {
					written += count
					if sendErr := s.send(protocol.Frame{Type: protocol.TypeWindowUpdate, StreamID: id, Payload: protocol.EncodeWindowUpdate(uint32(count))}); sendErr != nil {
						s.remove(id)
						return
					}
				}
				if err != nil {
					s.reset(id, err)
					return
				}
				if count == 0 {
					s.reset(id, io.ErrShortWrite)
					return
				}
			}
		case <-stream.done:
			return
		}
	}
}

func (s *Server) reset(id uint32, err error) {
	s.send(protocol.Frame{Type: protocol.TypeReset, StreamID: id, Payload: []byte(err.Error())})
	s.remove(id)
}

func (s *Server) send(frame protocol.Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.transport != nil {
		for {
			err := s.transport.WriteFrame(frame)
			if !errors.Is(err, framed.ErrDetached) {
				return err
			}
			if s.ctx == nil {
				return err
			}
			select {
			case <-s.ctx.Done():
				return s.ctx.Err()
			default:
			}
		}
	}
	return protocol.Write(s.rw, frame)
}

func (s *Server) remove(id uint32) {
	s.mu.Lock()
	stream := s.streams[id]
	delete(s.streams, id)
	s.mu.Unlock()
	if stream != nil {
		stream.closeOnce.Do(func() {
			close(stream.done)
			stream.cancel()
			if stream.conn != nil {
				_ = stream.conn.Close()
			}
		})
	}
}

func (s *Server) shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	listenerIDs := make([]uint32, 0, len(s.listeners))
	for id := range s.listeners {
		listenerIDs = append(listenerIDs, id)
	}
	s.mu.Unlock()
	for _, id := range listenerIDs {
		s.removeListener(id)
	}
	s.mu.Lock()
	datagramIDs := make([]uint32, 0, len(s.datagrams))
	for id := range s.datagrams {
		datagramIDs = append(datagramIDs, id)
	}
	s.mu.Unlock()
	for _, id := range datagramIDs {
		s.removeDatagram(id)
	}
	s.mu.Lock()
	reverseDatagramIDs := make([]uint32, 0, len(s.reverseDatagrams))
	for id := range s.reverseDatagrams {
		reverseDatagramIDs = append(reverseDatagramIDs, id)
	}
	s.mu.Unlock()
	for _, id := range reverseDatagramIDs {
		s.removeReverseDatagram(id)
	}
	s.mu.Lock()
	ids := make([]uint32, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.remove(id)
	}
}
