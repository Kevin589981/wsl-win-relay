package attach

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	wireVersion    byte = 1
	wireHeaderSize      = 12
	maxWirePayload      = 64 << 10
	maxWireToken        = 256
	maxWireError        = 4 << 10
)

var wireMagic = [4]byte{'W', 'W', 'A', '1'}

var ErrRemoteAttach = errors.New("remote attach rejected")

// MessageType identifies one attach-control message. Stream data continues
// to use the relay protocol after ATTACH_OK has been exchanged.
type MessageType byte

const (
	MessageHello MessageType = iota + 1
	MessageAttach
	MessageAttachOK
	MessageAttachError
	MessageRegistrySummary
	MessageResumeAck
)

// Message is a bounded control message for a broker/connector transport.
type Message struct {
	Type    MessageType
	Payload []byte
}

func (m Message) validate() error {
	if m.Type < MessageHello || m.Type > MessageResumeAck {
		return fmt.Errorf("unknown attach message type %d", m.Type)
	}
	if len(m.Payload) > maxWirePayload {
		return fmt.Errorf("attach payload exceeds %d bytes", maxWirePayload)
	}
	switch m.Type {
	case MessageHello:
		if len(m.Payload) != 8 {
			return errors.New("hello payload must contain capabilities")
		}
	case MessageAttach:
		if _, _, err := DecodeAttach(m.Payload); err != nil {
			return err
		}
	case MessageAttachOK:
		if len(m.Payload) != 16 {
			return errors.New("attach ok payload must contain epoch and capabilities")
		}
	case MessageAttachError:
		if len(m.Payload) == 0 || len(m.Payload) > maxWireError {
			return errors.New("attach error payload has invalid length")
		}
	case MessageRegistrySummary:
		if _, err := DecodeSummary(m.Payload); err != nil {
			return err
		}
	case MessageResumeAck:
		if _, _, err := DecodeResumeAck(m.Payload); err != nil {
			return err
		}
	}
	return nil
}

// Write serializes one complete attach-control message.
func Write(w io.Writer, m Message) error {
	if err := m.validate(); err != nil {
		return err
	}
	header := make([]byte, wireHeaderSize)
	copy(header[:4], wireMagic[:])
	header[4] = wireVersion
	header[5] = byte(m.Type)
	binary.BigEndian.PutUint32(header[8:], uint32(len(m.Payload)))
	if err := writeFull(w, header); err != nil {
		return err
	}
	return writeFull(w, m.Payload)
}

// Read decodes one complete attach-control message and rejects malformed or
// oversized input before allocating its payload.
func Read(r io.Reader) (Message, error) {
	header := make([]byte, wireHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return Message{}, err
	}
	if string(header[:4]) != string(wireMagic[:]) {
		return Message{}, errors.New("invalid attach protocol magic")
	}
	if header[4] != wireVersion {
		return Message{}, fmt.Errorf("unsupported attach protocol version %d", header[4])
	}
	length := binary.BigEndian.Uint32(header[8:])
	if length > maxWirePayload {
		return Message{}, fmt.Errorf("attach payload exceeds %d bytes", maxWirePayload)
	}
	m := Message{Type: MessageType(header[5]), Payload: make([]byte, length)}
	if _, err := io.ReadFull(r, m.Payload); err != nil {
		return Message{}, err
	}
	if err := m.validate(); err != nil {
		return Message{}, err
	}
	return m, nil
}

func EncodeCapabilities(capabilities uint64) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, capabilities)
	return payload
}

func DecodeCapabilities(payload []byte) (uint64, error) {
	if len(payload) != 8 {
		return 0, errors.New("capabilities payload must contain 8 bytes")
	}
	return binary.BigEndian.Uint64(payload), nil
}

// EncodeAttach includes the last successfully attached epoch so a future
// broker can decide whether resume is possible without changing the wire
// shape. The current implementation only authenticates and allocates a new
// generation.
func EncodeAttach(token []byte, lastEpoch uint64) ([]byte, error) {
	if len(token) == 0 || len(token) > maxWireToken {
		return nil, ErrInvalidToken
	}
	payload := make([]byte, 2+len(token)+8)
	binary.BigEndian.PutUint16(payload, uint16(len(token)))
	copy(payload[2:], token)
	binary.BigEndian.PutUint64(payload[2+len(token):], lastEpoch)
	return payload, nil
}

func DecodeAttach(payload []byte) ([]byte, uint64, error) {
	if len(payload) < 10 {
		return nil, 0, errors.New("attach payload is truncated")
	}
	tokenLength := int(binary.BigEndian.Uint16(payload))
	if tokenLength == 0 || tokenLength > maxWireToken || len(payload) != 2+tokenLength+8 {
		return nil, 0, errors.New("attach payload has invalid token length")
	}
	token := append([]byte(nil), payload[2:2+tokenLength]...)
	return token, binary.BigEndian.Uint64(payload[2+tokenLength:]), nil
}

func EncodeAttachOK(epoch, capabilities uint64) []byte {
	payload := make([]byte, 16)
	binary.BigEndian.PutUint64(payload, epoch)
	binary.BigEndian.PutUint64(payload[8:], capabilities)
	return payload
}

func DecodeAttachOK(payload []byte) (uint64, uint64, error) {
	if len(payload) != 16 {
		return 0, 0, errors.New("attach ok payload must contain 16 bytes")
	}
	return binary.BigEndian.Uint64(payload), binary.BigEndian.Uint64(payload[8:]), nil
}

type EntryKind byte

const (
	EntryStream EntryKind = iota + 1
	EntryReverseListener
	EntryDatagram
)

type EntryState byte

const (
	EntryActive EntryState = iota + 1
	EntryClosed
)

type RegistryEntry struct {
	ID    uint64
	Kind  EntryKind
	State EntryState
}

type Summary struct {
	Epoch   uint64
	Entries []RegistryEntry
}

const (
	maxSummaryEntries = 4096
	summaryHeaderSize = 10
	summaryEntrySize  = 10
)

func (e RegistryEntry) validate() error {
	if e.ID == 0 {
		return errors.New("registry entry id must be non-zero")
	}
	if e.Kind < EntryStream || e.Kind > EntryDatagram {
		return fmt.Errorf("unknown registry entry kind %d", e.Kind)
	}
	if e.State < EntryActive || e.State > EntryClosed {
		return fmt.Errorf("unknown registry entry state %d", e.State)
	}
	return nil
}

// EncodeSummary produces a deterministic epoch plus stable entry identifiers.
// Entries must be supplied in ascending ID order; rejecting unsorted input
// prevents peers from observing nondeterministic summaries.
func EncodeSummary(summary Summary) ([]byte, error) {
	if len(summary.Entries) > maxSummaryEntries || len(summary.Entries) > (maxWirePayload-summaryHeaderSize)/summaryEntrySize {
		return nil, errors.New("registry summary contains too many entries")
	}
	payload := make([]byte, summaryHeaderSize+summaryEntrySize*len(summary.Entries))
	binary.BigEndian.PutUint64(payload, summary.Epoch)
	binary.BigEndian.PutUint16(payload[8:], uint16(len(summary.Entries)))
	var previous uint64
	for index, entry := range summary.Entries {
		if err := entry.validate(); err != nil {
			return nil, err
		}
		if index > 0 && entry.ID <= previous {
			return nil, errors.New("registry summary entry ids must be strictly increasing")
		}
		previous = entry.ID
		offset := summaryHeaderSize + summaryEntrySize*index
		binary.BigEndian.PutUint64(payload[offset:], entry.ID)
		payload[offset+8] = byte(entry.Kind)
		payload[offset+9] = byte(entry.State)
	}
	return payload, nil
}

func DecodeSummary(payload []byte) (Summary, error) {
	if len(payload) < summaryHeaderSize || (len(payload)-summaryHeaderSize)%summaryEntrySize != 0 {
		return Summary{}, errors.New("registry summary has invalid length")
	}
	count := int(binary.BigEndian.Uint16(payload[8:]))
	if count > maxSummaryEntries || len(payload) != summaryHeaderSize+summaryEntrySize*count {
		return Summary{}, errors.New("registry summary has invalid entry count")
	}
	summary := Summary{Epoch: binary.BigEndian.Uint64(payload), Entries: make([]RegistryEntry, count)}
	for index := range summary.Entries {
		offset := summaryHeaderSize + summaryEntrySize*index
		entry := RegistryEntry{ID: binary.BigEndian.Uint64(payload[offset:]), Kind: EntryKind(payload[offset+8]), State: EntryState(payload[offset+9])}
		if err := entry.validate(); err != nil {
			return Summary{}, err
		}
		if index > 0 && entry.ID <= summary.Entries[index-1].ID {
			return Summary{}, errors.New("registry summary entry ids are not strictly increasing")
		}
		summary.Entries[index] = entry
	}
	return summary, nil
}

func EncodeResumeAck(epoch uint64, ids []uint64) ([]byte, error) {
	if len(ids) > maxSummaryEntries || len(ids) > (maxWirePayload-10)/8 {
		return nil, errors.New("resume acknowledgement contains too many entries")
	}
	payload := make([]byte, 10+8*len(ids))
	binary.BigEndian.PutUint64(payload, epoch)
	binary.BigEndian.PutUint16(payload[8:], uint16(len(ids)))
	var previous uint64
	for index, id := range ids {
		if id == 0 || (index > 0 && id <= previous) {
			return nil, errors.New("resume acknowledgement ids must be strictly increasing and non-zero")
		}
		previous = id
		binary.BigEndian.PutUint64(payload[10+8*index:], id)
	}
	return payload, nil
}

func DecodeResumeAck(payload []byte) (uint64, []uint64, error) {
	if len(payload) < 10 || (len(payload)-10)%8 != 0 {
		return 0, nil, errors.New("resume acknowledgement has invalid length")
	}
	count := int(binary.BigEndian.Uint16(payload[8:]))
	if count > maxSummaryEntries || len(payload) != 10+8*count {
		return 0, nil, errors.New("resume acknowledgement has invalid entry count")
	}
	ids := make([]uint64, count)
	var previous uint64
	for index := range ids {
		id := binary.BigEndian.Uint64(payload[10+8*index:])
		if id == 0 || (index > 0 && id <= previous) {
			return 0, nil, errors.New("resume acknowledgement ids are not strictly increasing and non-zero")
		}
		ids[index], previous = id, id
	}
	return binary.BigEndian.Uint64(payload), ids, nil
}

// ClientHandshake performs HELLO -> ATTACH and waits for ATTACH_OK.
func ClientHandshake(rw io.ReadWriter, token []byte, capabilities, lastEpoch uint64) (uint64, uint64, error) {
	payload, err := EncodeAttach(token, lastEpoch)
	if err != nil {
		return 0, 0, err
	}
	if err := Write(rw, Message{Type: MessageHello, Payload: EncodeCapabilities(capabilities)}); err != nil {
		return 0, 0, err
	}
	if err := Write(rw, Message{Type: MessageAttach, Payload: payload}); err != nil {
		return 0, 0, err
	}
	response, err := Read(rw)
	if err != nil {
		return 0, 0, err
	}
	if response.Type == MessageAttachError {
		return 0, 0, fmt.Errorf("%w: %s", ErrRemoteAttach, response.Payload)
	}
	if response.Type != MessageAttachOK {
		return 0, 0, fmt.Errorf("unexpected attach response %d", response.Type)
	}
	epoch, peerCapabilities, err := DecodeAttachOK(response.Payload)
	return epoch, peerCapabilities, err
}

// ServerHandshake authenticates one connector and installs a new registry
// generation. On authentication failure it sends a bounded error response.
func ServerHandshake(rw io.ReadWriter, registry *Registry, capabilities uint64) (*Attachment, uint64, uint64, error) {
	if registry == nil {
		return nil, 0, 0, errors.New("attach registry is nil")
	}
	hello, err := Read(rw)
	if err != nil {
		return nil, 0, 0, err
	}
	if hello.Type != MessageHello {
		return nil, 0, 0, fmt.Errorf("expected hello, got %d", hello.Type)
	}
	peerCapabilities, err := DecodeCapabilities(hello.Payload)
	if err != nil {
		return nil, 0, 0, err
	}
	attachMessage, err := Read(rw)
	if err != nil {
		return nil, 0, 0, err
	}
	if attachMessage.Type != MessageAttach {
		return nil, 0, 0, fmt.Errorf("expected attach, got %d", attachMessage.Type)
	}
	token, lastEpoch, err := DecodeAttach(attachMessage.Payload)
	if err != nil {
		return nil, 0, 0, err
	}
	attachment, err := registry.Attach(token)
	if err != nil {
		message := err.Error()
		if len(message) > maxWireError {
			message = message[:maxWireError]
		}
		_ = Write(rw, Message{Type: MessageAttachError, Payload: []byte(message)})
		return nil, peerCapabilities, lastEpoch, err
	}
	if err := Write(rw, Message{Type: MessageAttachOK, Payload: EncodeAttachOK(attachment.Epoch(), capabilities)}); err != nil {
		_ = attachment.Detach()
		return nil, peerCapabilities, lastEpoch, err
	}
	return attachment, peerCapabilities, lastEpoch, nil
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
