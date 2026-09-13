package protocol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFrameRoundTrip(t *testing.T) {
	want := Frame{Type: TypeData, StreamID: 42, Payload: []byte("hello")}
	var b bytes.Buffer
	if err := Write(&b, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(&b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestReverseDatagramFrameRoundTrip(t *testing.T) {
	want := Frame{Type: TypeListenDatagramData, StreamID: 7, Payload: []byte{0, 1, 'x'}}
	var b bytes.Buffer
	if err := Write(&b, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(&b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestListenOKCarriesOptionalBoundAddress(t *testing.T) {
	for _, kind := range []Type{TypeListenOK, TypeListenDatagramOK} {
		frame := Frame{Type: kind, StreamID: 7, Payload: []byte("127.0.0.1:49152")}
		var buffer bytes.Buffer
		if err := Write(&buffer, frame); err != nil {
			t.Fatal(err)
		}
		decoded, err := Read(&buffer)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded.Payload, frame.Payload) {
			t.Fatalf("type %d payload=%q", kind, decoded.Payload)
		}
	}
}

func TestHelloOKCarriesOptionalInstanceID(t *testing.T) {
	legacy := EncodeHelloOK(AllCapabilities, 0)
	capabilities, instanceID, err := DecodeHelloOK(legacy)
	if err != nil || capabilities != AllCapabilities || instanceID != 0 {
		t.Fatalf("legacy hello capabilities=%x instance=%x err=%v", capabilities, instanceID, err)
	}
	current := EncodeHelloOK(AllCapabilities, 42)
	capabilities, instanceID, err = DecodeHelloOK(current)
	if err != nil || capabilities != AllCapabilities || instanceID != 42 {
		t.Fatalf("extended hello capabilities=%x instance=%x err=%v", capabilities, instanceID, err)
	}
	if err := (Frame{Type: TypeHelloOK, Payload: current}).Validate(); err != nil {
		t.Fatalf("extended hello frame rejected: %v", err)
	}
}

func TestReadRejectsOversizedPayload(t *testing.T) {
	var b bytes.Buffer
	header := make([]byte, HeaderSize)
	copy(header[:4], magic[:])
	header[4] = Version
	header[5] = byte(TypeData)
	header[11] = 1
	header[12] = 0xFF
	header[13] = 0xFF
	header[14] = 0xFF
	header[15] = 0xFF
	b.Write(header)
	if _, err := Read(&b); err == nil {
		t.Fatal("expected oversized payload error")
	}
}

func TestReadRejectsInvalidMetadataBeforeReadingPayload(t *testing.T) {
	cases := []struct {
		name     string
		kind     Type
		streamID uint32
		length   uint32
		want     string
	}{
		{"error", TypeOpenError, 1, MaxErrorSize + 1, "error payload exceeds"},
		{"stream data", TypeData, 1, MaxDataSize + 1, "stream data must be"},
		{"datagram data", TypeDatagramData, 1, MaxDatagramSize + 1, "datagram data must be"},
		{"reverse datagram data", TypeListenDatagramData, 1, MaxDatagramSize + 1, "reverse datagram data must be"},
		{"open target", TypeOpen, 1, MaxTargetSize + 1, "open target must be"},
		{"empty control", TypeClose, 1, 1, "empty payload"},
		{"inbound", TypeInboundOpen, 1, 3, "listener id"},
		{"hello", TypeHello, 0, 7, "capabilities"},
		{"unknown", Type(99), 1, 1, "unknown frame type"},
		{"zero stream", TypeData, 0, 1, "stream id"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			header := make([]byte, HeaderSize)
			copy(header[:4], magic[:])
			header[4] = Version
			header[5] = byte(test.kind)
			binary.BigEndian.PutUint32(header[8:12], test.streamID)
			binary.BigEndian.PutUint32(header[12:16], test.length)
			if _, err := Read(bytes.NewReader(header)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Read returned %v", err)
			}
		})
	}
}

func TestValidateRejectsInvalidFrames(t *testing.T) {
	cases := []Frame{
		{Type: TypeData},
		{Type: Type(99), StreamID: 1},
		{Type: TypeOpen, StreamID: 1},
		{Type: TypeClose, StreamID: 1, Payload: []byte("x")},
		{Type: TypeHalfClose, StreamID: 1, Payload: []byte("x")},
		{Type: TypeListenDatagramOpen, StreamID: 1},
		{Type: TypeListenDatagramData, StreamID: 1, Payload: []byte{0, 0}},
		{Type: TypeListenOK, StreamID: 1, Payload: make([]byte, MaxTargetSize+1)},
		{Type: TypeListenError, StreamID: 1, Payload: make([]byte, MaxErrorSize+1)},
	}
	for _, tc := range cases {
		if err := tc.Validate(); err == nil {
			t.Fatalf("expected validation error for %#v", tc)
		}
	}
}

func TestErrorPayloadIsBounded(t *testing.T) {
	payload := ErrorPayload(fmt.Errorf("%s", strings.Repeat("x", MaxErrorSize+100)))
	if len(payload) != MaxErrorSize {
		t.Fatalf("payload length=%d", len(payload))
	}
	if ErrorPayload(nil) != nil {
		t.Fatal("nil error produced a payload")
	}
	unicodePayload := ErrorPayload(fmt.Errorf("%s", strings.Repeat("界", MaxErrorSize)))
	if len(unicodePayload) > MaxErrorSize || !utf8.Valid(unicodePayload) {
		t.Fatalf("Unicode payload length=%d valid=%v", len(unicodePayload), utf8.Valid(unicodePayload))
	}
	invalidPayload := ErrorPayload(fmt.Errorf("%s", string([]byte{'x', 0xff, 'y'})))
	if !utf8.Valid(invalidPayload) {
		t.Fatalf("invalid input remained invalid UTF-8: %q", invalidPayload)
	}
}
