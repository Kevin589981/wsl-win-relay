package broker

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
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
