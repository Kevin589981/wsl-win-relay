package broker

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/framed"
)

func TestBrokerAssignsStableSortedEntries(t *testing.T) {
	broker, err := New(0x40)
	if err != nil {
		t.Fatal(err)
	}
	first, err := broker.Register(attach.EntryStream)
	if err != nil {
		t.Fatal(err)
	}
	second, err := broker.Register(attach.EntryReverseListener)
	if err != nil {
		t.Fatal(err)
	}
	if first == 0 || second != first+1 {
		t.Fatalf("entry ids=%d,%d", first, second)
	}
	if err := broker.SetState(second, attach.EntryClosed); err != nil {
		t.Fatal(err)
	}
	summary := broker.Summary()
	if len(summary.Entries) != 2 || summary.Entries[0].ID != first || summary.Entries[1].State != attach.EntryClosed {
		t.Fatalf("summary=%+v", summary)
	}
	if err := broker.Remove(first); err != nil {
		t.Fatal(err)
	}
	if len(broker.Summary().Entries) != 1 {
		t.Fatal("removed entry remained in summary")
	}
}

func TestBrokerRegistryLimitRejectsOnlyNewEntries(t *testing.T) {
	broker, err := New(0)
	if err != nil {
		t.Fatal(err)
	}
	var first uint64
	for index := 0; index < MaxRegistryEntries; index++ {
		id, err := broker.Register(attach.EntryStream)
		if err != nil {
			t.Fatalf("register entry %d: %v", index, err)
		}
		if index == 0 {
			first = id
		}
	}
	if _, err := broker.Register(attach.EntryDatagram); !errors.Is(err, ErrRegistryFull) {
		t.Fatalf("register beyond limit err=%v", err)
	}
	summary := broker.Summary()
	if len(summary.Entries) != attach.MaxSummaryEntries {
		t.Fatal("registry saturation changed established entries")
	}
	if _, err := attach.EncodeSummary(summary); err != nil {
		t.Fatalf("encode saturated registry: %v", err)
	}
	if err := broker.Remove(first); err != nil {
		t.Fatal(err)
	}
	id, err := broker.Register(attach.EntryDatagram)
	if err != nil {
		t.Fatalf("register after removal: %v", err)
	}
	if id <= first {
		t.Fatalf("replacement entry id=%d, want greater than removed id=%d", id, first)
	}
}

func TestBrokerAcceptsResumeSessionAndRejectsStaleClose(t *testing.T) {
	broker, err := New(0x40)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := broker.Register(attach.EntryDatagram)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	serverDone := make(chan struct {
		session *Session
		err     error
	}, 1)
	go func() {
		session, err := broker.Accept(right)
		serverDone <- struct {
			session *Session
			err     error
		}{session, err}
	}()
	epoch, peerCaps, summary, err := attach.ClientResumeHandshake(left, broker.Token(), 0x12, 0)
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 1 || peerCaps != 0x40 || len(summary.Entries) != 1 || summary.Entries[0].ID != entry {
		t.Fatalf("client epoch=%d caps=%x summary=%+v", epoch, peerCaps, summary)
	}
	result := <-serverDone
	if result.err != nil || result.session == nil || result.session.Epoch() != epoch || result.session.PeerCapabilities() != 0x12 || !result.session.Current() {
		t.Fatalf("server result=%+v", result)
	}
	left2, right2 := net.Pipe()
	defer left2.Close()
	defer right2.Close()
	serverDone2 := make(chan *Session, 1)
	go func() {
		session, err := broker.Accept(right2)
		if err != nil {
			t.Errorf("second accept: %v", err)
			return
		}
		serverDone2 <- session
	}()
	if _, _, _, err := attach.ClientResumeHandshake(left2, broker.Token(), 0x12, epoch); err != nil {
		t.Fatal(err)
	}
	newSession := <-serverDone2
	if err := result.session.Close(); !errors.Is(err, attach.ErrStaleAttachment) {
		t.Fatalf("stale close err=%v", err)
	}
	if !newSession.Current() {
		t.Fatal("stale close released replacement session")
	}
	if err := newSession.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerAcceptTimesOutStalledHandshake(t *testing.T) {
	oldTimeout := brokerHandshakeTimeout
	brokerHandshakeTimeout = 10 * time.Millisecond
	defer func() { brokerHandshakeTimeout = oldTimeout }()
	broker, err := New(0x40)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	done := make(chan error, 1)
	go func() {
		_, err := broker.Accept(right)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled handshake unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stalled handshake did not time out")
	}
	if got := broker.registry.CurrentEpoch(); got != 0 {
		t.Fatalf("stalled handshake left current epoch %d", got)
	}
}

func TestBrokerCloseRejectsNewEntriesAndSessions(t *testing.T) {
	broker, err := New(0)
	if err != nil {
		t.Fatal(err)
	}
	broker.Close()
	if _, err := broker.Register(attach.EntryStream); !errors.Is(err, ErrBrokerClosed) {
		t.Fatalf("register after close err=%v", err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if _, err := broker.Accept(right); !errors.Is(err, ErrBrokerClosed) {
		t.Fatalf("accept after close err=%v", err)
	}
}

func TestBrokerServeTracksConnectorUntilCancellation(t *testing.T) {
	broker, err := New(0x40)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handlerEntered := make(chan *Session, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- broker.Serve(ctx, listener, func(ctx context.Context, session *Session, _ net.Conn) error {
			handlerEntered <- session
			<-ctx.Done()
			return nil
		})
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, _, err := attach.ClientResumeHandshake(conn, broker.Token(), 0x12, 0); err != nil {
		t.Fatal(err)
	}
	session := <-handlerEntered
	if !session.Current() {
		t.Fatal("served session is not current")
	}
	cancel()
	if err := <-serveDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve err=%v", err)
	}
	if session.Current() {
		t.Fatal("session remained current after broker cancellation")
	}
}

func TestBrokerServeAttachedReplacesLinkWithoutStoppingListener(t *testing.T) {
	broker, err := New(0x40)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	link := framed.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- broker.ServeAttached(ctx, listener, link) }()

	first, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := attach.ClientResumeHandshake(first, broker.Token(), 0, 0); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = protocol.Write(first, protocol.Frame{Type: protocol.TypeHello, Payload: protocol.EncodeCapabilities(0)})
	}()
	if frame, err := link.ReadFrame(); err != nil || frame.Type != protocol.TypeHello {
		t.Fatalf("first relay frame=%+v err=%v", frame, err)
	}
	_ = first.Close()
	if _, err := link.ReadFrame(); !errors.Is(err, framed.ErrDetached) {
		t.Fatalf("first detach err=%v", err)
	}

	second, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, _, _, err := attach.ClientResumeHandshake(second, broker.Token(), 0, 1); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = protocol.Write(second, protocol.Frame{Type: protocol.TypeHello, Payload: protocol.EncodeCapabilities(0)})
	}()
	if frame, err := link.ReadFrame(); err != nil || frame.Type != protocol.TypeHello {
		t.Fatalf("replacement relay frame=%+v err=%v", frame, err)
	}
	cancel()
	_ = second.Close()
	if err := <-serveDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve attached err=%v", err)
	}
}

func TestBrokerServeAttachedStalledHandshakeDoesNotBlockConnector(t *testing.T) {
	oldTimeout := brokerHandshakeTimeout
	brokerHandshakeTimeout = 5 * time.Second
	defer func() { brokerHandshakeTimeout = oldTimeout }()
	broker, err := New(0x40)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	link := framed.New()
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- broker.ServeAttached(ctx, listener, link) }()
	stalled, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	waitForBrokerConnections(t, broker, 1)

	connector, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	_ = connector.SetDeadline(time.Now().Add(time.Second))
	if _, _, _, err := attach.ClientResumeHandshake(connector, broker.Token(), 0, 0); err != nil {
		t.Fatalf("valid connector was blocked by stalled handshake: %v", err)
	}
	_ = connector.SetDeadline(time.Time{})
	go func() {
		_ = protocol.Write(connector, protocol.Frame{Type: protocol.TypeHello, Payload: protocol.EncodeCapabilities(0)})
	}()
	if frame, err := link.ReadFrame(); err != nil || frame.Type != protocol.TypeHello {
		t.Fatalf("relay frame=%+v err=%v", frame, err)
	}

	cancel()
	select {
	case err := <-serveDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve attached err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending handshake delayed broker shutdown")
	}
}

func TestBrokerConnectionTrackingHasHardLimit(t *testing.T) {
	broker, err := New(0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]net.Conn, 0, MaxConcurrentConnections)
	for index := 0; index < MaxConcurrentConnections; index++ {
		left, right := net.Pipe()
		defer right.Close()
		if !broker.trackConnection(left, MaxConcurrentConnections) {
			t.Fatalf("connection %d was rejected before the limit", index)
		}
		connections = append(connections, left)
	}
	extra, extraPeer := net.Pipe()
	defer extra.Close()
	defer extraPeer.Close()
	if broker.trackConnection(extra, MaxConcurrentConnections) {
		t.Fatal("connection beyond limit was tracked")
	}
	broker.closeConnections()
	for _, connection := range connections {
		broker.untrackConnection(connection)
	}
}

func TestBrokerServeAttachedDetachesWhenLinkIsClosed(t *testing.T) {
	broker, err := New(0x40)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	link := framed.New()
	if err := link.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- broker.ServeAttached(ctx, listener, link) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, _, err := attach.ClientResumeHandshake(conn, broker.Token(), 0, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for broker.registry.CurrentEpoch() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := broker.registry.CurrentEpoch(); got != 0 {
		t.Fatalf("failed link attach left current epoch %d", got)
	}
	cancel()
	if err := <-serveDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve attached err=%v", err)
	}
}

func waitForBrokerConnections(t *testing.T, broker *Broker, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		broker.mu.Lock()
		count := len(broker.connections)
		broker.mu.Unlock()
		if count == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("broker did not track %d pending connection(s)", want)
}
