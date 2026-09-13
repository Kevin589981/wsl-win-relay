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
		if len(m.Payload) != 16 && len(m.Payload) != 24 {
			return errors.New("attach ok payload must contain epoch, capabilities, and optional instance id")
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

// EncodeAttachOKWithInstance extends ATTACH_OK with the broker instance ID.
// The original 16-byte form remains valid for older peers.
func EncodeAttachOKWithInstance(epoch, capabilities, instanceID uint64) []byte {
	payload := make([]byte, 24)
	binary.BigEndian.PutUint64(payload, epoch)
	binary.BigEndian.PutUint64(payload[8:], capabilities)
	binary.BigEndian.PutUint64(payload[16:], instanceID)
	return payload
}

func DecodeAttachOK(payload []byte) (uint64, uint64, error) {
	if len(payload) != 16 && len(payload) != 24 {
		return 0, 0, errors.New("attach ok payload must contain 16 or 24 bytes")
	}
	return binary.BigEndian.Uint64(payload), binary.BigEndian.Uint64(payload[8:]), nil
}

func DecodeAttachOKWithInstance(payload []byte) (uint64, uint64, uint64, error) {
	epoch, capabilities, err := DecodeAttachOK(payload)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(payload) != 24 {
		return epoch, capabilities, 0, nil
	}
	return epoch, capabilities, binary.BigEndian.Uint64(payload[16:]), nil
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

func (s Summary) IDs() []uint64 {
	ids := make([]uint64, len(s.Entries))
	for index, entry := range s.Entries {
		ids[index] = entry.ID
	}
	return ids
}

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

func validateResumeAck(summary Summary, epoch uint64, ids []uint64) error {
	if epoch != summary.Epoch {
		return fmt.Errorf("resume epoch %d does not match summary epoch %d", epoch, summary.Epoch)
	}
	if len(ids) > len(summary.Entries) {
		return errors.New("resume acknowledgement contains unknown entries")
	}
	entryIndex := 0
	for _, id := range ids {
		for entryIndex < len(summary.Entries) && summary.Entries[entryIndex].ID < id {
			entryIndex++
		}
		if entryIndex == len(summary.Entries) || summary.Entries[entryIndex].ID != id {
			return fmt.Errorf("resume acknowledgement references unknown entry %d", id)
		}
		entryIndex++
	}
	return nil
}

// ClientResumeHandshake performs attach negotiation, receives a registry
// summary, and acknowledges every entry currently advertised by the broker.
func ClientResumeHandshake(rw io.ReadWriter, token []byte, capabilities, lastEpoch uint64) (uint64, uint64, Summary, error) {
	epoch, peerCapabilities, _, summary, err := ClientResumeHandshakeWithInstance(rw, token, capabilities, lastEpoch)
	return epoch, peerCapabilities, summary, err
}

func ClientResumeHandshakeWithInstance(rw io.ReadWriter, token []byte, capabilities, lastEpoch uint64) (uint64, uint64, uint64, Summary, error) {
	epoch, peerCapabilities, instanceID, err := ClientHandshakeWithInstance(rw, token, capabilities, lastEpoch)
	if err != nil {
		return 0, 0, 0, Summary{}, err
	}
	message, err := Read(rw)
	if err != nil {
		return 0, 0, 0, Summary{}, err
	}
	if message.Type != MessageRegistrySummary {
		return 0, 0, 0, Summary{}, fmt.Errorf("expected registry summary, got %d", message.Type)
	}
	summary, err := DecodeSummary(message.Payload)
	if err != nil {
		return 0, 0, 0, Summary{}, err
	}
	if err := validateResumeAck(summary, epoch, summary.IDs()); err != nil {
		return 0, 0, 0, Summary{}, err
	}
	payload, err := EncodeResumeAck(epoch, summary.IDs())
	if err != nil {
		return 0, 0, 0, Summary{}, err
	}
	if err := Write(rw, Message{Type: MessageResumeAck, Payload: payload}); err != nil {
		return 0, 0, 0, Summary{}, err
	}
	return epoch, peerCapabilities, instanceID, summary, nil
}

// ServerResumeHandshake completes attach negotiation and waits for the
// connector to acknowledge the exact summary advertised for this epoch.
func ServerResumeHandshake(rw io.ReadWriter, registry *Registry, capabilities uint64, summary Summary) (*Attachment, uint64, uint64, error) {
	attachment, peerCapabilities, lastEpoch, _, err := ServerResumeHandshakeWithInstance(rw, registry, capabilities, summary)
	return attachment, peerCapabilities, lastEpoch, err
}

func ServerResumeHandshakeWithInstance(rw io.ReadWriter, registry *Registry, capabilities uint64, summary Summary) (*Attachment, uint64, uint64, uint64, error) {
	if registry == nil {
		return nil, 0, 0, 0, errors.New("attach registry is nil")
	}
	request, err := ReadServerAttachRequest(rw)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	return CompleteServerResumeHandshakeWithInstance(rw, registry, capabilities, summary, request)
}

// CompleteServerResumeHandshakeWithInstance authenticates a previously read
// request, installs its registry generation, and completes summary/ack.
func CompleteServerResumeHandshakeWithInstance(rw io.ReadWriter, registry *Registry, capabilities uint64, summary Summary, request ServerAttachRequest) (*Attachment, uint64, uint64, uint64, error) {
	attachment, peerCapabilities, lastEpoch, instanceID, err := completeServerHandshakeWithInstance(rw, registry, capabilities, request)
	if err != nil {
		return nil, peerCapabilities, lastEpoch, 0, err
	}
	summary.Epoch = attachment.Epoch()
	payload, err := EncodeSummary(summary)
	if err != nil {
		_ = attachment.Detach()
		return nil, peerCapabilities, lastEpoch, instanceID, err
	}
	if err := Write(rw, Message{Type: MessageRegistrySummary, Payload: payload}); err != nil {
		_ = attachment.Detach()
		return nil, peerCapabilities, lastEpoch, instanceID, err
	}
	message, err := Read(rw)
	if err != nil {
		_ = attachment.Detach()
		return nil, peerCapabilities, lastEpoch, instanceID, err
	}
	if message.Type != MessageResumeAck {
		_ = attachment.Detach()
		return nil, peerCapabilities, lastEpoch, instanceID, fmt.Errorf("expected resume acknowledgement, got %d", message.Type)
	}
	ackEpoch, ids, err := DecodeResumeAck(message.Payload)
	if err != nil {
		_ = attachment.Detach()
		return nil, peerCapabilities, lastEpoch, instanceID, err
	}
	if err := validateResumeAck(summary, ackEpoch, ids); err != nil {
		_ = attachment.Detach()
		return nil, peerCapabilities, lastEpoch, instanceID, err
	}
	return attachment, peerCapabilities, lastEpoch, instanceID, nil
}

// ServerAttachRequest is the bounded, decoded request prefix received before a
// registry generation is installed.
type ServerAttachRequest struct {
	Token            []byte
	PeerCapabilities uint64
	LastEpoch        uint64
}

// ReadServerAttachRequest reads HELLO and ATTACH without mutating a registry.
// A broker can run this bounded I/O concurrently, then serialize completion.
func ReadServerAttachRequest(rw io.Reader) (ServerAttachRequest, error) {
	hello, err := Read(rw)
	if err != nil {
		return ServerAttachRequest{}, err
	}
	if hello.Type != MessageHello {
		return ServerAttachRequest{}, fmt.Errorf("expected hello, got %d", hello.Type)
	}
	peerCapabilities, err := DecodeCapabilities(hello.Payload)
	if err != nil {
		return ServerAttachRequest{}, err
	}
	attachMessage, err := Read(rw)
	if err != nil {
		return ServerAttachRequest{}, err
	}
	if attachMessage.Type != MessageAttach {
		return ServerAttachRequest{}, fmt.Errorf("expected attach, got %d", attachMessage.Type)
	}
	token, lastEpoch, err := DecodeAttach(attachMessage.Payload)
	if err != nil {
		return ServerAttachRequest{}, err
	}
	return ServerAttachRequest{Token: token, PeerCapabilities: peerCapabilities, LastEpoch: lastEpoch}, nil
}

// ClientHandshake performs HELLO -> ATTACH and waits for ATTACH_OK.
func ClientHandshake(rw io.ReadWriter, token []byte, capabilities, lastEpoch uint64) (uint64, uint64, error) {
	epoch, peerCapabilities, _, err := ClientHandshakeWithInstance(rw, token, capabilities, lastEpoch)
	return epoch, peerCapabilities, err
}

func ClientHandshakeWithInstance(rw io.ReadWriter, token []byte, capabilities, lastEpoch uint64) (uint64, uint64, uint64, error) {
	payload, err := EncodeAttach(token, lastEpoch)
	if err != nil {
		return 0, 0, 0, err
	}
	if err := Write(rw, Message{Type: MessageHello, Payload: EncodeCapabilities(capabilities)}); err != nil {
		return 0, 0, 0, err
	}
	if err := Write(rw, Message{Type: MessageAttach, Payload: payload}); err != nil {
		return 0, 0, 0, err
	}
	response, err := Read(rw)
	if err != nil {
		return 0, 0, 0, err
	}
	if response.Type == MessageAttachError {
		return 0, 0, 0, fmt.Errorf("%w: %s", ErrRemoteAttach, response.Payload)
	}
	if response.Type != MessageAttachOK {
		return 0, 0, 0, fmt.Errorf("unexpected attach response %d", response.Type)
	}
	return DecodeAttachOKWithInstance(response.Payload)
}

// ServerHandshake authenticates one connector and installs a new registry
// generation. On authentication failure it sends a bounded error response.
func ServerHandshake(rw io.ReadWriter, registry *Registry, capabilities uint64) (*Attachment, uint64, uint64, error) {
	attachment, peerCapabilities, lastEpoch, _, err := ServerHandshakeWithInstance(rw, registry, capabilities)
	return attachment, peerCapabilities, lastEpoch, err
}

func ServerHandshakeWithInstance(rw io.ReadWriter, registry *Registry, capabilities uint64) (*Attachment, uint64, uint64, uint64, error) {
	if registry == nil {
		return nil, 0, 0, 0, errors.New("attach registry is nil")
	}
	request, err := ReadServerAttachRequest(rw)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	return completeServerHandshakeWithInstance(rw, registry, capabilities, request)
}

func completeServerHandshakeWithInstance(rw io.Writer, registry *Registry, capabilities uint64, request ServerAttachRequest) (*Attachment, uint64, uint64, uint64, error) {
	if registry == nil {
		return nil, request.PeerCapabilities, request.LastEpoch, 0, errors.New("attach registry is nil")
	}
	attachment, err := registry.Attach(request.Token)
	if err != nil {
		message := err.Error()
		if len(message) > maxWireError {
			message = message[:maxWireError]
		}
		_ = Write(rw, Message{Type: MessageAttachError, Payload: []byte(message)})
		return nil, request.PeerCapabilities, request.LastEpoch, 0, err
	}
	if err := Write(rw, Message{Type: MessageAttachOK, Payload: EncodeAttachOKWithInstance(attachment.Epoch(), capabilities, registry.InstanceID())}); err != nil {
		_ = attachment.Detach()
		return nil, request.PeerCapabilities, request.LastEpoch, 0, err
	}
	return attachment, request.PeerCapabilities, request.LastEpoch, registry.InstanceID(), nil
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
