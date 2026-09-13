package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	Version             uint8 = 1
	HeaderSize                = 16
	MaxPayloadSize            = 1 << 20
	MaxTargetSize             = 4096
	MaxErrorSize              = 4096
	MaxDataSize               = 32 << 10
	MaxDatagramSize           = 2 + MaxTargetSize + 65535
	InitialStreamWindow       = 256 << 10
)

var magic = [4]byte{'W', 'W', 'R', '1'}

type Type uint8

const (
	TypeOpen Type = iota + 1
	TypeOpenOK
	TypeOpenError
	TypeData
	TypeHalfClose
	TypeClose
	TypeReset
	TypeListenOpen
	TypeListenOK
	TypeListenError
	TypeInboundOpen
	TypeListenClose
	TypeListenCommit
	TypeDatagramOpen
	TypeDatagramOK
	TypeDatagramError
	TypeDatagramData
	TypeDatagramClose
	TypeWindowUpdate
	TypeHello
	TypeHelloOK
	TypeListenDatagramOpen
	TypeListenDatagramOK
	TypeListenDatagramError
	TypeListenDatagramData
	TypeListenDatagramClose
)

type Frame struct {
	Type     Type
	StreamID uint32
	Payload  []byte
}

func (f Frame) Validate() error {
	return validateFrameMetadata(f.Type, f.StreamID, uint64(len(f.Payload)))
}

func validateFrameMetadata(kind Type, streamID uint32, payloadLength uint64) error {
	if streamID == 0 && kind != TypeHello && kind != TypeHelloOK {
		return errors.New("stream id must be non-zero")
	}
	if (kind == TypeHello || kind == TypeHelloOK) && streamID != 0 {
		return errors.New("hello frames must use stream id zero")
	}
	if !knownType(kind) {
		return fmt.Errorf("unknown frame type %d", kind)
	}
	if payloadLength > MaxPayloadSize {
		return fmt.Errorf("payload exceeds %d bytes", MaxPayloadSize)
	}
	if (kind == TypeOpenOK || kind == TypeHalfClose || kind == TypeClose || kind == TypeListenClose || kind == TypeListenCommit || kind == TypeDatagramOpen || kind == TypeDatagramOK || kind == TypeDatagramClose || kind == TypeListenDatagramClose) && payloadLength != 0 {
		return fmt.Errorf("frame type %d must have an empty payload", kind)
	}
	if (kind == TypeListenOK || kind == TypeListenDatagramOK) && payloadLength > MaxTargetSize {
		return fmt.Errorf("bound listen address exceeds %d bytes", MaxTargetSize)
	}
	if isErrorType(kind) && payloadLength > MaxErrorSize {
		return fmt.Errorf("error payload exceeds %d bytes", MaxErrorSize)
	}
	if kind == TypeOpen && (payloadLength == 0 || payloadLength > MaxTargetSize) {
		return fmt.Errorf("open target must be between 1 and %d bytes", MaxTargetSize)
	}
	if kind == TypeListenOpen && (payloadLength == 0 || payloadLength > MaxTargetSize) {
		return fmt.Errorf("listen address must be between 1 and %d bytes", MaxTargetSize)
	}
	if kind == TypeListenDatagramOpen && (payloadLength == 0 || payloadLength > MaxTargetSize) {
		return fmt.Errorf("datagram listen address must be between 1 and %d bytes", MaxTargetSize)
	}
	if kind == TypeInboundOpen && payloadLength != 4 {
		return errors.New("inbound open must contain a listener id")
	}
	if kind == TypeDatagramData && (payloadLength < 3 || payloadLength > MaxDatagramSize) {
		return fmt.Errorf("datagram data must be between 3 and %d bytes", MaxDatagramSize)
	}
	if kind == TypeListenDatagramData && (payloadLength < 3 || payloadLength > MaxDatagramSize) {
		return fmt.Errorf("reverse datagram data must be between 3 and %d bytes", MaxDatagramSize)
	}
	if kind == TypeData && (payloadLength == 0 || payloadLength > MaxDataSize) {
		return fmt.Errorf("stream data must be between 1 and %d bytes", MaxDataSize)
	}
	if kind == TypeWindowUpdate && payloadLength != 4 {
		return errors.New("window update must contain a byte count")
	}
	if kind == TypeHello && payloadLength != 8 {
		return errors.New("hello frame must contain capabilities")
	}
	if kind == TypeHelloOK && payloadLength != 8 && payloadLength != 16 {
		return errors.New("hello response must contain 8 or 16 bytes")
	}
	return nil
}

func isErrorType(kind Type) bool {
	switch kind {
	case TypeOpenError, TypeReset, TypeListenError, TypeDatagramError, TypeListenDatagramError:
		return true
	default:
		return false
	}
}

// ErrorPayload bounds locally generated diagnostics before they enter a
// control frame. Receive-side validation independently rejects oversized peers.
func ErrorPayload(err error) []byte {
	if err == nil {
		return nil
	}
	payload := []byte(err.Error())
	if len(payload) > MaxErrorSize {
		payload = payload[:MaxErrorSize]
		for !utf8.Valid(payload) {
			payload = payload[:len(payload)-1]
		}
	}
	return payload
}

func Write(w io.Writer, f Frame) error {
	if err := f.Validate(); err != nil {
		return err
	}
	header := make([]byte, HeaderSize)
	copy(header[:4], magic[:])
	header[4] = Version
	header[5] = byte(f.Type)
	binary.BigEndian.PutUint32(header[8:12], f.StreamID)
	binary.BigEndian.PutUint32(header[12:16], uint32(len(f.Payload)))
	if err := writeFull(w, header); err != nil {
		return err
	}
	return writeFull(w, f.Payload)
}

func Read(r io.Reader) (Frame, error) {
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return Frame{}, err
	}
	if string(header[:4]) != string(magic[:]) {
		return Frame{}, errors.New("invalid protocol magic")
	}
	if header[4] != Version {
		return Frame{}, fmt.Errorf("unsupported protocol version %d", header[4])
	}
	kind := Type(header[5])
	streamID := binary.BigEndian.Uint32(header[8:12])
	length := binary.BigEndian.Uint32(header[12:16])
	if err := validateFrameMetadata(kind, streamID, uint64(length)); err != nil {
		return Frame{}, err
	}
	f := Frame{Type: kind, StreamID: streamID, Payload: make([]byte, length)}
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return Frame{}, err
	}
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

func knownType(t Type) bool {
	return t >= TypeOpen && t <= TypeListenDatagramClose
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
