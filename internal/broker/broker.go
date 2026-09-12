// Package broker contains the transport-independent Windows broker state.
// Socket ownership and relay frame dispatch can be layered on top of this
// session boundary without changing attach authentication or reconciliation.
package broker

import (
	"errors"
	"io"
	"sort"
	"sync"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
)

var (
	ErrBrokerClosed = errors.New("broker is closed")
	ErrUnknownEntry = errors.New("unknown broker entry")
)

type Broker struct {
	mu           sync.Mutex
	registry     *attach.Registry
	capabilities uint64
	nextID       uint64
	entries      map[uint64]attach.RegistryEntry
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
	return &Broker{registry: registry, capabilities: capabilities, entries: make(map[uint64]attach.RegistryEntry)}
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
	attachment, peerCapabilities, lastEpoch, err := attach.ServerResumeHandshake(rw, b.registry, capabilities, summary)
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
	return &Session{broker: b, attachment: attachment, peerCapabilities: peerCapabilities, lastEpoch: lastEpoch}, nil
}

func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()
	if b.registry != nil {
		b.registry.Close()
	}
}

type Session struct {
	broker           *Broker
	attachment       *attach.Attachment
	peerCapabilities uint64
	lastEpoch        uint64
	closeOnce        sync.Once
	closeErr         error
}

func (s *Session) Epoch() uint64 { return s.attachment.Epoch() }

func (s *Session) PeerCapabilities() uint64 { return s.peerCapabilities }

func (s *Session) LastEpoch() uint64 { return s.lastEpoch }

func (s *Session) Current() bool { return s.attachment.Current() }

func (s *Session) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.attachment.Detach() })
	return s.closeErr
}
