package framed

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
)

func TestLinkWaitsForAttachmentAndPreservesFrameBoundary(t *testing.T) {
	link := New()
	readDone := make(chan struct {
		frame protocol.Frame
		err   error
	}, 1)
	go func() {
		frame, err := link.ReadFrame()
		readDone <- struct {
			frame protocol.Frame
			err   error
		}{frame, err}
	}()
	left, right := net.Pipe()
	attachment, err := link.Attach(left)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = protocol.Write(right, protocol.Frame{Type: protocol.TypeData, StreamID: 2, Payload: []byte("ok")})
	}()
	result := <-readDone
	if result.err != nil || string(result.frame.Payload) != "ok" {
		t.Fatalf("result=%+v", result)
	}
	if !attachment.Current() {
		t.Fatal("attachment was not current")
	}
	_ = attachment.Close()
	_ = right.Close()
}

func TestStaleAttachmentCannotDetachReplacement(t *testing.T) {
	link := New()
	firstReader, firstWriter := net.Pipe()
	first, err := link.Attach(firstReader)
	if err != nil {
		t.Fatal(err)
	}
	secondReader, secondWriter := net.Pipe()
	second, err := link.Attach(secondReader)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Detach(); !errors.Is(err, ErrStale) {
		t.Fatalf("stale detach err=%v", err)
	}
	if !second.Current() {
		t.Fatal("stale attachment detached replacement")
	}
	_ = firstWriter.Close()
	_ = secondWriter.Close()
	_ = link.Close()
}

type blockingCloseTransport struct {
	closeStarted chan struct{}
	closeRelease chan struct{}
}

func (b *blockingCloseTransport) Read([]byte) (int, error)    { return 0, io.EOF }
func (b *blockingCloseTransport) Write(p []byte) (int, error) { return len(p), nil }
func (b *blockingCloseTransport) Close() error {
	select {
	case <-b.closeStarted:
	default:
		close(b.closeStarted)
	}
	<-b.closeRelease
	return nil
}

func TestAttachDoesNotHoldLockWhileClosingPreviousTransport(t *testing.T) {
	link := New()
	old := &blockingCloseTransport{closeStarted: make(chan struct{}), closeRelease: make(chan struct{})}
	if _, err := link.Attach(old); err != nil {
		t.Fatal(err)
	}

	replacement := make(chan error, 1)
	go func() {
		_, err := link.Attach(&bytes.Buffer{})
		replacement <- err
	}()
	select {
	case <-old.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("previous transport was not closed")
	}
	select {
	case err := <-replacement:
		// The replacement attach closes the old transport before returning, so
		// it is expected to remain blocked until the close is released.
		t.Fatalf("replacement attach returned while previous Close was blocked: %v", err)
	default:
	}

	attached := make(chan error, 1)
	go func() {
		_, err := link.Attach(&bytes.Buffer{})
		attached <- err
	}()
	select {
	case err := <-attached:
		if err != nil {
			t.Fatalf("replacement attach failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement attach blocked on previous Close")
	}
	close(old.closeRelease)
	select {
	case err := <-replacement:
		if err != nil {
			t.Fatalf("replacement attach failed after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement attach did not finish after previous Close released")
	}
	_ = link.Close()
}

func TestLinkReadReportsDetachAndCanResume(t *testing.T) {
	link := New()
	firstReader, firstWriter := net.Pipe()
	if _, err := link.Attach(firstReader); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := link.ReadFrame()
		readDone <- err
	}()
	_ = firstWriter.Close()
	if err := <-readDone; !errors.Is(err, ErrDetached) {
		t.Fatalf("read err=%v", err)
	}
	secondReader, secondWriter := net.Pipe()
	if _, err := link.Attach(secondReader); err != nil {
		t.Fatal(err)
	}
	go func() { _ = protocol.Write(secondWriter, protocol.Frame{Type: protocol.TypeOpenOK, StreamID: 2}) }()
	if frame, err := link.ReadFrame(); err != nil || frame.Type != protocol.TypeOpenOK {
		t.Fatalf("resume frame=%+v err=%v", frame, err)
	}
	_ = secondWriter.Close()
	_ = link.Close()
}

func TestConcurrentWritesRemainDecodable(t *testing.T) {
	link := New()
	reader, writer := net.Pipe()
	if _, err := link.Attach(writer); err != nil {
		t.Fatal(err)
	}
	const count = 32
	var group sync.WaitGroup
	group.Add(count)
	for i := 0; i < count; i++ {
		go func(i int) {
			defer group.Done()
			_ = link.WriteFrame(protocol.Frame{Type: protocol.TypeData, StreamID: uint32(i + 1), Payload: []byte("x")})
		}(i)
	}
	for i := 0; i < count; i++ {
		frame, err := protocol.Read(reader)
		if err != nil || frame.Type != protocol.TypeData || !bytes.Equal(frame.Payload, []byte("x")) {
			t.Fatalf("frame=%+v err=%v", frame, err)
		}
	}
	group.Wait()
	_ = reader.Close()
	_ = link.Close()
}

func TestCloseUnblocksReadersAndRejectsAttach(t *testing.T) {
	link := New()
	done := make(chan error, 1)
	go func() {
		_, err := link.ReadFrame()
		done <- err
	}()
	if err := link.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("read after close err=%v", err)
	}
	if _, err := link.Attach(&bytes.Buffer{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("attach after close err=%v", err)
	}
}

func TestWriteFrameContextHonorsCancellationWhileDetached(t *testing.T) {
	link := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- link.WriteFrameContext(ctx, protocol.Frame{Type: protocol.TypeData, Payload: []byte("blocked")})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not unblock write")
	}
	_ = link.Close()
}
