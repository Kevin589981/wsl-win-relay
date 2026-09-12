package attach

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

func TestWireRoundTripAndPartialWrites(t *testing.T) {
	attachPayload, err := EncodeAttach([]byte("secret"), 42)
	if err != nil {
		t.Fatal(err)
	}
	message := Message{Type: MessageAttach, Payload: attachPayload}
	var wire partialWriter
	if err := Write(&wire, message); err != nil {
		t.Fatal(err)
	}
	got, err := Read(bytes.NewReader(wire.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != message.Type || !bytes.Equal(got.Payload, message.Payload) {
		t.Fatalf("message=%+v, want %+v", got, message)
	}
}

func TestWireRejectsMalformedMessages(t *testing.T) {
	var header [wireHeaderSize]byte
	copy(header[:4], wireMagic[:])
	header[4] = wireVersion
	binary.BigEndian.PutUint32(header[8:], maxWirePayload+1)
	if _, err := Read(bytes.NewReader(header[:])); err == nil {
		t.Fatal("oversized payload was accepted")
	}
	if err := Write(io.Discard, Message{Type: MessageHello, Payload: []byte{1}}); err == nil {
		t.Fatal("malformed hello was accepted")
	}
	if _, _, err := DecodeAttach([]byte{0, 1}); err == nil {
		t.Fatal("truncated attach was accepted")
	}
	if _, err := EncodeAttach(make([]byte, maxWireToken+1), 0); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("oversized token err=%v", err)
	}
}

func TestHandshakeInstallsGenerationAndReturnsPeerState(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	serverResult := make(chan struct {
		attachment *Attachment
		peerCaps   uint64
		lastEpoch  uint64
		err        error
	}, 1)
	go func() {
		attachment, peerCaps, lastEpoch, err := ServerHandshake(right, registry, 0x30)
		serverResult <- struct {
			attachment *Attachment
			peerCaps   uint64
			lastEpoch  uint64
			err        error
		}{attachment, peerCaps, lastEpoch, err}
	}()
	epoch, peerCaps, err := ClientHandshake(left, []byte("secret"), 0x12, 7)
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 1 || peerCaps != 0x30 {
		t.Fatalf("client epoch=%d caps=%x", epoch, peerCaps)
	}
	result := <-serverResult
	if result.err != nil || result.attachment == nil || result.peerCaps != 0x12 || result.lastEpoch != 7 {
		t.Fatalf("server result=%+v", result)
	}
	if !result.attachment.Current() {
		t.Fatal("server attachment is not current")
	}
}

func TestHandshakeRejectsInvalidTokenWithoutReplacingCurrent(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	current, _ := registry.Attach([]byte("secret"))
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	serverDone := make(chan error, 1)
	go func() {
		_, _, _, err := ServerHandshake(right, registry, 0)
		serverDone <- err
	}()
	if _, _, err := ClientHandshake(left, []byte("wrong"), 0, current.Epoch()); !errors.Is(err, ErrRemoteAttach) {
		t.Fatalf("client err=%v", err)
	}
	if err := <-serverDone; !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("server err=%v", err)
	}
	if !current.Current() || registry.CurrentEpoch() != current.Epoch() {
		t.Fatal("invalid attach replaced current generation")
	}
}

func TestServerHandshakeDetachesWhenResponseWriteFails(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	attachPayload, err := EncodeAttach([]byte("secret"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if err := Write(&input, Message{Type: MessageHello, Payload: EncodeCapabilities(0)}); err != nil {
		t.Fatal(err)
	}
	if err := Write(&input, Message{Type: MessageAttach, Payload: attachPayload}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = ServerHandshake(failingReadWriter{Reader: bytes.NewReader(input.Bytes())}, registry, 0)
	if err == nil {
		t.Fatal("failed response write unexpectedly completed handshake")
	}
	if registry.CurrentEpoch() != 0 {
		t.Fatal("failed handshake left an attached generation")
	}
}

type partialWriter struct{ bytes.Buffer }

func (w *partialWriter) Write(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return w.Buffer.Write(p)
}

type failingReadWriter struct {
	*bytes.Reader
}

func (failingReadWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
