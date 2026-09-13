package protocol

import (
	"bytes"
	"testing"
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
	}
	for _, tc := range cases {
		if err := tc.Validate(); err == nil {
			t.Fatalf("expected validation error for %#v", tc)
		}
	}
}
