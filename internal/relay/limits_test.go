package relay

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
)

func TestClientRejectsLocalResourcesAtRegistryLimits(t *testing.T) {
	tests := []struct {
		name string
		fill func(*Client)
		open func(*Client) error
	}{
		{"streams", func(c *Client) { fillClientStreams(c, MaxConcurrentStreams) }, func(c *Client) error { _, err := c.DialContext(context.Background(), "example.test:443"); return err }},
		{"listeners", func(c *Client) {
			for i := 0; i < MaxConcurrentListeners; i++ {
				c.listeners[uint32(i+1)] = nil
			}
		}, func(c *Client) error {
			_, err := c.ReserveReverseForward(context.Background(), "127.0.0.1:8000", "127.0.0.1:8000")
			return err
		}},
		{"datagrams", func(c *Client) {
			for i := 0; i < MaxConcurrentDatagrams; i++ {
				c.datagrams[uint32(i+1)] = nil
			}
		}, func(c *Client) error { _, err := c.OpenPacketContext(context.Background()); return err }},
		{"reverse datagrams", func(c *Client) {
			for i := 0; i < MaxConcurrentReverseDatagrams; i++ {
				c.reverseDatagrams[uint32(i+1)] = nil
			}
		}, func(c *Client) error {
			_, err := c.ReverseDatagramForward(context.Background(), "127.0.0.1:5353", "127.0.0.1:5353")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(&discardReadWriter{})
			test.fill(client)
			if err := test.open(client); !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("open returned %v", err)
			}
		})
	}
}

func TestServerRejectsPeerResourcesAtRegistryLimits(t *testing.T) {
	tests := []struct {
		name     string
		kind     protocol.Type
		prepare  func(*Server, *atomic.Int32)
		open     func(*Server)
		mapCount func(*Server) int
		maximum  int
	}{
		{"streams", protocol.TypeOpenError, func(s *Server, calls *atomic.Int32) {
			fillServerStreams(s, MaxConcurrentStreams)
			s.dial = func(context.Context, string) (net.Conn, error) {
				calls.Add(1)
				return nil, errors.New("unexpected dial")
			}
		}, func(s *Server) { s.open(10000, "example.test:443") }, func(s *Server) int { return len(s.streams) }, MaxConcurrentStreams},
		{"listeners", protocol.TypeListenError, func(s *Server, _ *atomic.Int32) {
			for i := 0; i < MaxConcurrentListeners; i++ {
				s.listeners[uint32(i+1)] = nil
			}
		}, func(s *Server) { s.openListener(10000, "127.0.0.1:0") }, func(s *Server) int { return len(s.listeners) }, MaxConcurrentListeners},
		{"datagrams", protocol.TypeDatagramError, func(s *Server, calls *atomic.Int32) {
			for i := 0; i < MaxConcurrentDatagrams; i++ {
				s.datagrams[uint32(i+1)] = nil
			}
			s.packetDial = func(context.Context) (net.PacketConn, error) {
				calls.Add(1)
				return nil, errors.New("unexpected packet dial")
			}
		}, func(s *Server) { s.openDatagram(10000) }, func(s *Server) int { return len(s.datagrams) }, MaxConcurrentDatagrams},
		{"reverse datagrams", protocol.TypeListenDatagramError, func(s *Server, calls *atomic.Int32) {
			for i := 0; i < MaxConcurrentReverseDatagrams; i++ {
				s.reverseDatagrams[uint32(i+1)] = nil
			}
			s.resolveUDP = func(context.Context, string, string) (*net.UDPAddr, error) {
				calls.Add(1)
				return nil, errors.New("unexpected resolve")
			}
		}, func(s *Server) { s.openReverseDatagram(10000, "127.0.0.1:0") }, func(s *Server) int { return len(s.reverseDatagrams) }, MaxConcurrentReverseDatagrams},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := &lockedBuffer{}
			server := NewServer(capture, nil)
			server.ctx = context.Background()
			var calls atomic.Int32
			test.prepare(server, &calls)
			test.open(server)
			if calls.Load() != 0 {
				t.Fatalf("resource allocation called %d times", calls.Load())
			}
			if got := test.mapCount(server); got != test.maximum {
				t.Fatalf("registry size=%d, want %d", got, test.maximum)
			}
			frame, err := protocol.Read(bytes.NewReader(capture.Snapshot()))
			if err != nil {
				t.Fatal(err)
			}
			if frame.Type != test.kind || !strings.Contains(string(frame.Payload), ErrResourceLimit.Error()) {
				t.Fatalf("frame=%#v", frame)
			}
		})
	}
}

func TestClientRejectsInboundStreamAtLimit(t *testing.T) {
	capture := &lockedBuffer{}
	client := NewClient(capture)
	listenerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.listeners[7] = &clientListener{id: 7, client: client, target: "127.0.0.1:8000", ctx: listenerCtx, cancel: cancel, ready: make(chan listenerReady, 1)}
	fillClientStreams(client, MaxConcurrentStreams)
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, 7)
	client.dispatch(protocol.Frame{Type: protocol.TypeInboundOpen, StreamID: 10000, Payload: payload})
	frame, err := protocol.Read(bytes.NewReader(capture.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != protocol.TypeReset || !strings.Contains(string(frame.Payload), ErrResourceLimit.Error()) {
		t.Fatalf("frame=%#v", frame)
	}
}

func TestServerRejectsAcceptedStreamAtLimit(t *testing.T) {
	server := NewServer(&discardReadWriter{}, nil)
	fillServerStreams(server, MaxConcurrentStreams)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	if _, _, accepted := server.registerInboundStream(local); accepted {
		t.Fatal("accepted inbound stream beyond registry limit")
	}
	if len(server.streams) != MaxConcurrentStreams {
		t.Fatalf("registry size=%d", len(server.streams))
	}
}

func TestServerRejectsOpenFramesWhenWorkerGateIsFull(t *testing.T) {
	tests := []struct {
		request protocol.Frame
		want    protocol.Type
	}{
		{protocol.Frame{Type: protocol.TypeOpen, StreamID: 1, Payload: []byte("example.test:443")}, protocol.TypeOpenError},
		{protocol.Frame{Type: protocol.TypeListenOpen, StreamID: 3, Payload: []byte("127.0.0.1:8000")}, protocol.TypeListenError},
		{protocol.Frame{Type: protocol.TypeDatagramOpen, StreamID: 5}, protocol.TypeDatagramError},
	}
	for _, test := range tests {
		capture := &lockedBuffer{}
		server := NewServer(capture, nil)
		for index := 0; index < MaxConcurrentOpenOperations; index++ {
			server.openSlots <- struct{}{}
		}
		server.handle(test.request)
		frame, err := protocol.Read(bytes.NewReader(capture.Snapshot()))
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type != test.want || !strings.Contains(string(frame.Payload), ErrResourceLimit.Error()) {
			t.Fatalf("frame=%#v", frame)
		}
	}
}

func TestListenerReleasesOpenWorkerAfterBind(t *testing.T) {
	server := NewServer(&discardReadWriter{}, nil)
	server.ctx = context.Background()
	server.handle(protocol.Frame{Type: protocol.TypeListenOpen, StreamID: 1, Payload: []byte("127.0.0.1:0")})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		listener := server.listeners[1]
		ready := listener != nil && listener.listener != nil
		server.mu.Unlock()
		if ready && len(server.openSlots) == 0 {
			server.removeListener(1)
			return
		}
		time.Sleep(time.Millisecond)
	}
	server.removeListener(1)
	t.Fatalf("listener registration did not release open worker; slots=%d", len(server.openSlots))
}

func fillClientStreams(client *Client, count int) {
	for index := 0; index < count; index++ {
		client.streams[uint32(index+1)] = nil
	}
}

func fillServerStreams(server *Server, count int) {
	for index := 0; index < count; index++ {
		server.streams[uint32(index+1)] = nil
	}
}
