// Package broker contains the transport-independent Windows broker state.
// Socket ownership and relay frame dispatch can be layered on top of this
// session boundary without changing attach authentication or reconciliation.
package broker

import (
	"context"
	"errors"
	"io"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/framed"
)

var (
	ErrBrokerClosed = errors.New("broker is closed")
	ErrUnknownEntry = errors.New("unknown broker entry")
)

var brokerHandshakeTimeout = 15 * time.Second

type Broker struct {
	mu           sync.Mutex
	registry     *attach.Registry
	capabilities uint64
	nextID       uint64
	entries      map[uint64]attach.RegistryEntry
	connections  map[net.Conn]struct{}
	closed       bool
}

func New(capabilities uint64) (*Broker, error) {
	registry, err := attach.New()
	if err != nil {
		return nil, err
	}
	return NewWithRegistry(registry, capabilities), nil
}

func NewWithRegistry(registry *attach.Registry, capabilities uint64) *Broker {
	return &Broker{registry: registry, capabilities: capabilities, entries: make(map[uint64]attach.RegistryEntry), connections: make(map[net.Conn]struct{})}
}

func (b *Broker) Token() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.registry == nil {
		return nil
	}
	return b.registry.Token()
}

func (b *Broker) Register(kind attach.EntryKind) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.registry == nil {
		return 0, ErrBrokerClosed
	}
	for {
		b.nextID++
		if b.nextID != 0 {
			break
		}
	}
	entry := attach.RegistryEntry{ID: b.nextID, Kind: kind, State: attach.EntryActive}
	if _, err := attach.EncodeSummary(attach.Summary{Entries: []attach.RegistryEntry{entry}}); err != nil {
		return 0, err
	}
	b.entries[entry.ID] = entry
	return entry.ID, nil
}

func (b *Broker) SetState(id uint64, state attach.EntryState) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.registry == nil {
		return ErrBrokerClosed
	}
	entry, ok := b.entries[id]
	if !ok {
		return ErrUnknownEntry
	}
	entry.State = state
	if _, err := attach.EncodeSummary(attach.Summary{Entries: []attach.RegistryEntry{entry}}); err != nil {
		return err
	}
	b.entries[id] = entry
	return nil
}

func (b *Broker) Remove(id uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.registry == nil {
		return ErrBrokerClosed
	}
	if _, ok := b.entries[id]; !ok {
		return ErrUnknownEntry
	}
	delete(b.entries, id)
	return nil
}

func (b *Broker) Summary() attach.Summary {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.registry == nil {
		return attach.Summary{}
	}
	entries := make([]attach.RegistryEntry, 0, len(b.entries))
	for _, entry := range b.entries {
		entries = append(entries, entry)
	}
	// EncodeSummary requires ascending IDs. Sorting here also makes every
	// connector observe the same summary independent of map iteration order.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].ID < entries[j-1].ID; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	return attach.Summary{Epoch: b.registry.CurrentEpoch(), Entries: entries}
}

// Accept runs the attach/resume handshake against one connector. It does not
// start relay frame dispatch; the returned session is the ownership boundary
// for that next layer.
func (b *Broker) Accept(rw io.ReadWriter) (*Session, error) {
	clearDeadline := setHandshakeDeadline(rw, brokerHandshakeTimeout)
	defer clearDeadline()
	b.mu.Lock()
	if b.closed || b.registry == nil {
		b.mu.Unlock()
		return nil, ErrBrokerClosed
	}
	capabilities := b.capabilities
	entries := make([]attach.RegistryEntry, 0, len(b.entries))
	for _, entry := range b.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	b.mu.Unlock()
	summary := attach.Summary{Entries: entries}
	attachment, peerCapabilities, lastEpoch, instanceID, err := attach.ServerResumeHandshakeWithInstance(rw, b.registry, capabilities, summary)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		_ = attachment.Detach()
		return nil, ErrBrokerClosed
	}
	return &Session{broker: b, attachment: attachment, peerCapabilities: peerCapabilities, lastEpoch: lastEpoch, instanceID: instanceID}, nil
}

func setHandshakeDeadline(rw io.ReadWriter, timeout time.Duration) func() {
	conn, ok := rw.(interface{ SetDeadline(time.Time) error })
	if !ok || timeout <= 0 {
		return func() {}
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return func() {}
	}
	return func() { _ = conn.SetDeadline(time.Time{}) }
}

// Serve accepts connector transports until ctx is canceled or the listener
// fails. The handler owns the session after the resume handshake returns; a
// nil handler keeps an attached session alive until cancellation, which is
// useful for a broker health endpoint before relay dispatch is installed.
func (b *Broker) Serve(ctx context.Context, listener net.Listener, handler func(context.Context, *Session, net.Conn) error) error {
	if listener == nil {
		return errors.New("broker listener is nil")
	}
	serveCtx, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup
	defer func() {
		cancel()
		_ = listener.Close()
		b.closeConnections()
		handlers.Wait()
	}()
	go func() {
		<-serveCtx.Done()
		_ = listener.Close()
		b.closeConnections()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if serveCtx.Err() != nil {
				return serveCtx.Err()
			}
			if networkErr, ok := err.(net.Error); ok && networkErr.Temporary() {
				continue
			}
			return err
		}
		b.trackConnection(conn)
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			b.serveConnection(serveCtx, conn, handler)
		}()
	}
}

// ServeAttached accepts connector control handshakes and installs each
// resulting transport into link. The link itself is consumed by a single
// relay.Server.ServeAttached loop, so replacing a connector does not shut down
// broker-owned sockets.
func (b *Broker) ServeAttached(ctx context.Context, listener net.Listener, link *framed.Link) error {
	return b.ServeAttachedWith(ctx, listener, link, nil)
}

// ServeAttachedWith is ServeAttached with an optional callback invoked after
// the attach handshake and before the connector transport is installed.
func (b *Broker) ServeAttachedWith(ctx context.Context, listener net.Listener, link *framed.Link, onAttach func(*Session)) error {
	if listener == nil {
		return errors.New("broker listener is nil")
	}
	if link == nil {
		return errors.New("broker frame link is nil")
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		_ = listener.Close()
		_ = link.Close()
		b.closeConnections()
	}()
	go func() {
		select {
		case <-serveCtx.Done():
			_ = listener.Close()
			_ = link.Close()
			b.closeConnections()
		}
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if serveCtx.Err() != nil {
				return serveCtx.Err()
			}
			if networkErr, ok := err.(net.Error); ok && networkErr.Temporary() {
				continue
			}
			return err
		}
		session, err := b.Accept(conn)
		if err != nil {
			_ = conn.Close()
			continue
		}
		if onAttach != nil {
			onAttach(session)
		}
		if _, err := link.Attach(conn); err != nil {
			// The handshake installed a registry attachment before the frame
			// link accepted the transport. Release it on this failure path so a
			// closed link cannot leave a phantom current generation behind.
			_ = session.Close()
			_ = conn.Close()
			continue
		}
	}
}

func (b *Broker) serveConnection(ctx context.Context, conn net.Conn, handler func(context.Context, *Session, net.Conn) error) {
	defer func() {
		b.untrackConnection(conn)
		_ = conn.Close()
	}()
	session, err := b.Accept(conn)
	if err != nil {
		return
	}
	defer session.Close()
	if handler != nil {
		_ = handler(ctx, session, conn)
		return
	}
	<-ctx.Done()
}

func (b *Broker) trackConnection(conn net.Conn) {
	b.mu.Lock()
	if b.connections == nil {
		b.connections = make(map[net.Conn]struct{})
	}
	b.connections[conn] = struct{}{}
	b.mu.Unlock()
}

func (b *Broker) untrackConnection(conn net.Conn) {
	b.mu.Lock()
	delete(b.connections, conn)
	b.mu.Unlock()
}

func (b *Broker) closeConnections() {
	b.mu.Lock()
	connections := make([]net.Conn, 0, len(b.connections))
	for conn := range b.connections {
		connections = append(connections, conn)
	}
	b.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()
	b.closeConnections()
	if b.registry != nil {
		b.registry.Close()
	}
}

type Session struct {
	broker           *Broker
	attachment       *attach.Attachment
	peerCapabilities uint64
	lastEpoch        uint64
	instanceID       uint64
	closeOnce        sync.Once
	closeErr         error
}

func (s *Session) Epoch() uint64 { return s.attachment.Epoch() }

func (s *Session) PeerCapabilities() uint64 { return s.peerCapabilities }

func (s *Session) LastEpoch() uint64 { return s.lastEpoch }

func (s *Session) InstanceID() uint64 { return s.instanceID }

func (s *Session) Current() bool { return s.attachment.Current() }

func (s *Session) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.attachment.Detach() })
	return s.closeErr
}
