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
)

type DialContextFunc func(context.Context, string) (net.Conn, error)

type Server struct {
	rw         io.ReadWriter
	dial       DialContextFunc
	writeMu    sync.Mutex
	mu         sync.Mutex
	streams    map[uint32]*serverStream
	listeners  map[uint32]*serverListener
	datagrams  map[uint32]*serverDatagram
	nextStream atomic.Uint32
	ctx        context.Context
	cancel     context.CancelFunc
}

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
	return &Server{rw: rw, dial: dial, streams: make(map[uint32]*serverStream), listeners: make(map[uint32]*serverListener), datagrams: make(map[uint32]*serverDatagram)}
}

func (s *Server) Serve(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)
	defer s.shutdown()
	for {
		frame, err := protocol.Read(s.rw)
		if err != nil {
			return err
		}
		s.handle(frame)
	}
}

func (s *Server) handle(frame protocol.Frame) {
	switch frame.Type {
	case protocol.TypeOpen:
		go s.open(frame.StreamID, string(frame.Payload))
	case protocol.TypeListenOpen:
		go s.openListener(frame.StreamID, string(frame.Payload))
	case protocol.TypeListenClose:
		s.removeListener(frame.StreamID)
	case protocol.TypeListenCommit:
		s.commitListener(frame.StreamID)
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

type serverDatagram struct{ conn *net.UDPConn }

func (s *Server) openDatagram(id uint32) {
	conn, err := net.ListenUDP("udp", nil)
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
	s.datagrams[id] = &serverDatagram{conn: conn}
	s.mu.Unlock()
	if err := s.send(protocol.Frame{Type: protocol.TypeDatagramOK, StreamID: id}); err != nil {
		s.removeDatagram(id)
		return
	}
	go s.readDatagrams(id, conn)
}

func (s *Server) writeDatagram(id uint32, payload []byte) {
	target, data, err := protocol.DecodeDatagram(payload)
	if err != nil {
		s.datagramError(id, err)
		return
	}
	address, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		s.datagramError(id, err)
		return
	}
	s.mu.Lock()
	datagram := s.datagrams[id]
	s.mu.Unlock()
	if datagram == nil {
		return
	}
	if _, err := datagram.conn.WriteToUDP(data, address); err != nil {
		s.datagramError(id, err)
	}
}

func (s *Server) readDatagrams(id uint32, conn *net.UDPConn) {
	buffer := make([]byte, 65535)
	for {
		count, source, err := conn.ReadFromUDP(buffer)
		if err != nil {
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
func (s *Server) removeDatagram(id uint32) {
	s.mu.Lock()
	datagram := s.datagrams[id]
	delete(s.datagrams, id)
	s.mu.Unlock()
	if datagram != nil {
		_ = datagram.conn.Close()
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
	s.mu.Lock()
	if _, exists := s.listeners[id]; exists {
		s.mu.Unlock()
		cancel()
		_ = ln.Close()
		return
	}
	s.listeners[id] = &serverListener{listener: ln, cancel: cancel, commit: make(chan struct{})}
	s.mu.Unlock()
	if err := s.send(protocol.Frame{Type: protocol.TypeListenOK, StreamID: id}); err != nil {
		s.removeListener(id)
		return
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	s.mu.Lock()
	registered := s.listeners[id]
	s.mu.Unlock()
	select {
	case <-registered.commit:
	case <-ctx.Done():
		return
	}
	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
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
			if _, creditErr := stream.sendWindow.take(n, stream.done, time.Time{}); creditErr != nil {
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
	ids := make([]uint32, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.remove(id)
	}
}
