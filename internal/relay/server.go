package relay

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"

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
	nextStream atomic.Uint32
	ctx        context.Context
	cancel     context.CancelFunc
}

type serverStream struct {
	conn   net.Conn
	cancel context.CancelFunc
}

func NewServer(rw io.ReadWriter, dial DialContextFunc) *Server {
	if dial == nil {
		d := &net.Dialer{}
		dial = func(ctx context.Context, target string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", target)
		}
	}
	return &Server{rw: rw, dial: dial, streams: make(map[uint32]*serverStream), listeners: make(map[uint32]*serverListener)}
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
	case protocol.TypeData:
		s.mu.Lock()
		stream := s.streams[frame.StreamID]
		s.mu.Unlock()
		if stream != nil && stream.conn != nil {
			if _, err := stream.conn.Write(frame.Payload); err != nil {
				s.reset(frame.StreamID, err)
			}
		}
	case protocol.TypeHalfClose:
		s.mu.Lock()
		stream := s.streams[frame.StreamID]
		s.mu.Unlock()
		if stream != nil && stream.conn != nil {
			if cw, ok := stream.conn.(interface{ CloseWrite() error }); ok {
				if err := cw.CloseWrite(); err != nil {
					s.reset(frame.StreamID, err)
				}
			}
		}
	case protocol.TypeClose, protocol.TypeReset:
		s.remove(frame.StreamID)
	}
}

type serverListener struct {
	listener net.Listener
	cancel   context.CancelFunc
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
	s.listeners[id] = &serverListener{listener: ln, cancel: cancel}
	s.mu.Unlock()
	if err := s.send(protocol.Frame{Type: protocol.TypeListenOK, StreamID: id}); err != nil {
		s.removeListener(id)
		return
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		streamID := s.nextStream.Add(2)
		stream := &serverStream{conn: conn, cancel: func() {}}
		s.mu.Lock()
		s.streams[streamID] = stream
		s.mu.Unlock()
		payload := make([]byte, 4)
		binary.BigEndian.PutUint32(payload, id)
		if err := s.send(protocol.Frame{Type: protocol.TypeInboundOpen, StreamID: streamID, Payload: payload}); err != nil {
			s.remove(streamID)
			return
		}
		go s.copyToClient(streamID, conn)
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
	stream := &serverStream{cancel: cancel}
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
		stream.cancel()
		if stream.conn != nil {
			stream.conn.Close()
		}
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
	ids := make([]uint32, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.remove(id)
	}
}
