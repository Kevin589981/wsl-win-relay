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
	}
	for _, tc := range cases {
		if err := tc.Validate(); err == nil {
			t.Fatalf("expected validation error for %#v", tc)
		}
	}
}
