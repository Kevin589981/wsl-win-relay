package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	Version             uint8 = 1
	HeaderSize                = 16
	MaxPayloadSize            = 1 << 20
	MaxTargetSize             = 4096
	MaxDataSize               = 32 << 10
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
	if f.StreamID == 0 && f.Type != TypeHello && f.Type != TypeHelloOK {
		return errors.New("stream id must be non-zero")
	}
	if (f.Type == TypeHello || f.Type == TypeHelloOK) && f.StreamID != 0 {
		return errors.New("hello frames must use stream id zero")
	}
	if !knownType(f.Type) {
		return fmt.Errorf("unknown frame type %d", f.Type)
	}
	if len(f.Payload) > MaxPayloadSize {
		return fmt.Errorf("payload exceeds %d bytes", MaxPayloadSize)
	}
	if (f.Type == TypeOpenOK || f.Type == TypeHalfClose || f.Type == TypeClose || f.Type == TypeListenOK || f.Type == TypeListenClose || f.Type == TypeListenCommit || f.Type == TypeDatagramOpen || f.Type == TypeDatagramOK || f.Type == TypeDatagramClose || f.Type == TypeListenDatagramOK || f.Type == TypeListenDatagramClose) && len(f.Payload) != 0 {
		return fmt.Errorf("frame type %d must have an empty payload", f.Type)
	}
	if f.Type == TypeOpen && (len(f.Payload) == 0 || len(f.Payload) > MaxTargetSize) {
		return fmt.Errorf("open target must be between 1 and %d bytes", MaxTargetSize)
	}
	if f.Type == TypeListenOpen && (len(f.Payload) == 0 || len(f.Payload) > MaxTargetSize) {
		return fmt.Errorf("listen address must be between 1 and %d bytes", MaxTargetSize)
	}
	if f.Type == TypeListenDatagramOpen && (len(f.Payload) == 0 || len(f.Payload) > MaxTargetSize) {
		return fmt.Errorf("datagram listen address must be between 1 and %d bytes", MaxTargetSize)
	}
	if f.Type == TypeInboundOpen && len(f.Payload) != 4 {
		return errors.New("inbound open must contain a listener id")
	}
	if f.Type == TypeDatagramData && len(f.Payload) < 3 {
		return errors.New("datagram data must contain an endpoint and payload")
	}
	if f.Type == TypeListenDatagramData && len(f.Payload) < 3 {
		return errors.New("reverse datagram data must contain an endpoint and payload")
	}
	if f.Type == TypeData && (len(f.Payload) == 0 || len(f.Payload) > MaxDataSize) {
		return fmt.Errorf("stream data must be between 1 and %d bytes", MaxDataSize)
	}
	if f.Type == TypeWindowUpdate && len(f.Payload) != 4 {
		return errors.New("window update must contain a byte count")
	}
	if (f.Type == TypeHello || f.Type == TypeHelloOK) && len(f.Payload) != 8 {
		return errors.New("hello frame must contain capabilities")
	}
	return nil
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
	length := binary.BigEndian.Uint32(header[12:16])
	if length > MaxPayloadSize {
		return Frame{}, fmt.Errorf("payload exceeds %d bytes", MaxPayloadSize)
	}
	f := Frame{Type: Type(header[5]), StreamID: binary.BigEndian.Uint32(header[8:12]), Payload: make([]byte, length)}
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
