package attach

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestSummaryAndResumeAckRoundTrip(t *testing.T) {
	summary := Summary{Epoch: 9, Entries: []RegistryEntry{
		{ID: 4, Kind: EntryStream, State: EntryActive},
		{ID: 11, Kind: EntryReverseListener, State: EntryClosed},
	}}
	payload, err := EncodeSummary(summary)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSummary(payload)
	if err != nil || decoded.Epoch != summary.Epoch || len(decoded.Entries) != 2 || decoded.Entries[1] != summary.Entries[1] {
		t.Fatalf("summary=%+v err=%v", decoded, err)
	}
	ackPayload, err := EncodeResumeAck(9, []uint64{4, 11})
	if err != nil {
		t.Fatal(err)
	}
	epoch, ids, err := DecodeResumeAck(ackPayload)
	if err != nil || epoch != 9 || len(ids) != 2 || ids[1] != 11 {
		t.Fatalf("ack epoch=%d ids=%v err=%v", epoch, ids, err)
	}
	var wire bytes.Buffer
	if err := Write(&wire, Message{Type: MessageRegistrySummary, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := Write(&wire, Message{Type: MessageResumeAck, Payload: ackPayload}); err != nil {
		t.Fatal(err)
	}
	if message, err := Read(&wire); err != nil || message.Type != MessageRegistrySummary {
		t.Fatalf("summary message=%+v err=%v", message, err)
	}
	if message, err := Read(&wire); err != nil || message.Type != MessageResumeAck {
		t.Fatalf("ack message=%+v err=%v", message, err)
	}
}

func TestSummaryRejectsUnsortedAndInvalidEntries(t *testing.T) {
	if _, err := EncodeSummary(Summary{Entries: []RegistryEntry{{ID: 2, Kind: EntryStream, State: EntryActive}, {ID: 1, Kind: EntryStream, State: EntryActive}}}); err == nil {
		t.Fatal("unsorted summary was accepted")
	}
	if _, err := EncodeSummary(Summary{Entries: []RegistryEntry{{ID: 1, Kind: 9, State: EntryActive}}}); err == nil {
		t.Fatal("invalid summary kind was accepted")
	}
	if _, err := EncodeResumeAck(1, []uint64{2, 2}); err == nil {
		t.Fatal("duplicate resume id was accepted")
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

func TestHandshakeWithInstanceReturnsStableBrokerIdentity(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	serverResult := make(chan uint64, 1)
	go func() {
		attachment, _, _, instanceID, err := ServerHandshakeWithInstance(right, registry, 0)
		if attachment != nil {
			defer attachment.Detach()
		}
		if err != nil {
			serverResult <- 0
			return
		}
		serverResult <- instanceID
	}()
	_, _, instanceID, err := ClientHandshakeWithInstance(left, []byte("secret"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if instanceID == 0 || instanceID != <-serverResult || instanceID != registry.InstanceID() {
		t.Fatalf("instance id=%d registry=%d", instanceID, registry.InstanceID())
	}
}

func TestResumeHandshakeExchangesSummaryAndAcknowledgement(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	wantSummary := Summary{Entries: []RegistryEntry{{ID: 4, Kind: EntryStream, State: EntryActive}, {ID: 8, Kind: EntryDatagram, State: EntryClosed}}}
	serverResult := make(chan struct {
		attachment *Attachment
		peerCaps   uint64
		lastEpoch  uint64
		err        error
	}, 1)
	go func() {
		attachment, peerCaps, lastEpoch, err := ServerResumeHandshake(right, registry, 0x30, wantSummary)
		serverResult <- struct {
			attachment *Attachment
			peerCaps   uint64
			lastEpoch  uint64
			err        error
		}{attachment, peerCaps, lastEpoch, err}
	}()
	epoch, peerCaps, summary, err := ClientResumeHandshake(left, []byte("secret"), 0x12, 3)
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 1 || peerCaps != 0x30 || summary.Epoch != epoch || len(summary.Entries) != 2 {
		t.Fatalf("client epoch=%d caps=%x summary=%+v", epoch, peerCaps, summary)
	}
	result := <-serverResult
	if result.err != nil || result.attachment == nil || result.peerCaps != 0x12 || result.lastEpoch != 3 || !result.attachment.Current() {
		t.Fatalf("server result=%+v", result)
	}
}

func TestResumeHandshakeRejectsWrongEpochAndDetaches(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	serverDone := make(chan error, 1)
	go func() {
		_, _, _, err := ServerResumeHandshake(right, registry, 0, Summary{})
		serverDone <- err
	}()
	if _, _, err := ClientHandshake(left, []byte("secret"), 0, 0); err != nil {
		t.Fatal(err)
	}
	message, err := Read(left)
	if err != nil || message.Type != MessageRegistrySummary {
		t.Fatalf("summary message=%+v err=%v", message, err)
	}
	payload, err := EncodeResumeAck(99, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(left, Message{Type: MessageResumeAck, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err == nil {
		t.Fatal("wrong resume epoch was accepted")
	}
	if registry.CurrentEpoch() != 0 {
		t.Fatal("failed resume left an attached generation")
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

func TestAttachErrorPayloadIsSingleLineBoundedUTF8(t *testing.T) {
	payload := attachErrorPayload(fmt.Errorf("first\r\n%s", strings.Repeat("\u754c", maxWireError)))
	if len(payload) > maxWireError || !utf8.Valid(payload) {
		t.Fatalf("payload length=%d valid=%v", len(payload), utf8.Valid(payload))
	}
	if bytes.ContainsAny(payload, "\r\n") || !bytes.HasPrefix(payload, []byte("first  ")) {
		t.Fatalf("payload framing=%q", payload)
	}
	if attachErrorPayload(nil) != nil {
		t.Fatal("nil error produced an attach error payload")
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
