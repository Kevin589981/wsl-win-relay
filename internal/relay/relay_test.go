package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/framed"
)

func TestClientServerEcho(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, func(context.Context, string) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() { _, _ = io.Copy(remote, remote); remote.Close() }()
		return local, nil
	})
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(ctx) }()
	client := NewClient(clientSide)
	clientDone := make(chan error, 1)
	go func() { clientDone <- client.Run(ctx) }()

	conn, err := client.DialContext(ctx, "example.invalid:1234")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("got %q", buf)
	}
	conn.Close()
	client.Close()
	serverSide.Close()
	clientSide.Close()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestAttachedClientAndServerPreserveStreamAcrossReplacement(t *testing.T) {
	clientLink := framed.New()
	serverLink := framed.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServerWithLink(serverLink, func(context.Context, string) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			buffer := make([]byte, 32*1024)
			for {
				count, err := remote.Read(buffer)
				if count > 0 {
					if _, writeErr := remote.Write(buffer[:count]); writeErr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		return local, nil
	}, nil)
	client := NewClientWithLink(clientLink)
	serverDone := make(chan error, 1)
	clientDone := make(chan error, 1)
	go func() { serverDone <- server.ServeAttached(ctx) }()
	go func() { clientDone <- client.RunAttached(ctx) }()

	clientEndpoint, serverEndpoint := net.Pipe()
	if _, err := clientLink.Attach(clientEndpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := serverLink.Attach(serverEndpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Handshake(ctx, protocol.CapabilityTCP); err != nil {
		t.Fatal(err)
	}
	stream, err := client.DialContext(ctx, "attached.example:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("before"))
	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "before" {
		t.Fatalf("before read=%q err=%v", buffer, err)
	}

	newClientEndpoint, newServerEndpoint := net.Pipe()
	if _, err := clientLink.Attach(newClientEndpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := serverLink.Attach(newServerEndpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Rehandshake(ctx, protocol.CapabilityTCP); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	buffer = make([]byte, len("after"))
	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "after" {
		t.Fatalf("after read=%q err=%v", buffer, err)
	}
	_ = stream.Close()
	_ = client.Close()
	_ = serverLink.Close()
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("attached client did not stop")
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("attached server did not stop")
	}
}

func TestHandshakeIsIdempotent(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()
	for attempt := 0; attempt < 2; attempt++ {
		capabilities, err := client.Handshake(ctx, protocol.CapabilityTCP|protocol.CapabilityUDP)
		if err != nil {
			t.Fatal(err)
		}
		if capabilities != protocol.AllCapabilities {
			t.Fatalf("capabilities 0x%x", capabilities)
		}
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestClientRunRejectsSecondReader(t *testing.T) {
	client := NewClient(&discardReadWriter{})
	if err := client.Run(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("first run: %v", err)
	}
	if err := client.Run(context.Background()); !errors.Is(err, ErrClientAlreadyRunning) {
		t.Fatalf("second run: %v", err)
	}
}

func TestServerServeRejectsSecondReader(t *testing.T) {
	server := NewServer(&discardReadWriter{}, nil)
	if err := server.Serve(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("first serve: %v", err)
	}
	if err := server.Serve(context.Background()); !errors.Is(err, ErrServerAlreadyRunning) {
		t.Fatalf("second serve: %v", err)
	}
}

func TestServerServeAttachedSurvivesConnectorReplacement(t *testing.T) {
	link := framed.New()
	server := NewServerWithLink(link, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeAttached(ctx) }()

	firstReader, firstWriter := net.Pipe()
	if _, err := link.Attach(firstReader); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = protocol.Write(firstWriter, protocol.Frame{Type: protocol.TypeHello, Payload: protocol.EncodeCapabilities(protocol.CapabilityTCP)})
	}()
	if frame, err := protocol.Read(firstWriter); err != nil || frame.Type != protocol.TypeHelloOK {
		t.Fatalf("first hello response=%+v err=%v", frame, err)
	}
	_ = firstWriter.Close()

	secondReader, secondWriter := net.Pipe()
	if _, err := link.Attach(secondReader); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = protocol.Write(secondWriter, protocol.Frame{Type: protocol.TypeHello, Payload: protocol.EncodeCapabilities(protocol.CapabilityTCP)})
	}()
	if frame, err := protocol.Read(secondWriter); err != nil || frame.Type != protocol.TypeHelloOK {
		t.Fatalf("replacement hello response=%+v err=%v", frame, err)
	}
	cancel()
	_ = secondWriter.Close()
	if err := <-serverDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve attached err=%v", err)
	}
}

func TestStreamCloseUnblocksRead(t *testing.T) {
	writer := &blockingWriteReadWriter{unblock: make(chan struct{})}
	stream := newClientStream(NewClient(writer), 1, "example:1")
	result := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 1))
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	closeDone := make(chan struct{})
	go func() {
		_ = stream.Close()
		close(closeDone)
	}()
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read after close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream read remained blocked after close")
	}
	close(writer.unblock)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("stream close remained blocked after writer release")
	}
}

func TestPacketCloseUnblocksRead(t *testing.T) {
	writer := &blockingWriteReadWriter{unblock: make(chan struct{})}
	packet := &clientPacketConn{client: NewClient(writer), id: 1, ready: make(chan error, 1), incoming: make(chan packetEvent, 1), done: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, _, err := packet.ReadFrom(make([]byte, 1))
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	closeDone := make(chan struct{})
	go func() {
		_ = packet.Close()
		close(closeDone)
	}()
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read after close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("packet read remained blocked after close")
	}
	close(writer.unblock)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("packet close remained blocked after writer release")
	}
}

func TestClientRunCancellationClosesTransport(t *testing.T) {
	transport := &blockingReadCloser{unblock: make(chan struct{}), closed: make(chan struct{})}
	client := NewClient(transport)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.Run(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client run did not stop after cancellation")
	}
	select {
	case <-transport.closed:
	case <-time.After(time.Second):
		t.Fatal("transport was not closed")
	}
}

func TestServerServeCancellationClosesTransport(t *testing.T) {
	transport := &blockingReadCloser{unblock: make(chan struct{}), closed: make(chan struct{})}
	server := NewServer(transport, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("server run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after cancellation")
	}
	select {
	case <-transport.closed:
	case <-time.After(time.Second):
		t.Fatal("server did not close transport")
	}
}

func TestReverseForward(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	go func() {
		conn, acceptErr := local.Accept()
		if acceptErr == nil {
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
		}
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	windowsAddr := probe.Addr().String()
	_ = probe.Close()

	reservationCtx, reservationCancel := context.WithCancel(ctx)
	forward, err := client.ReverseForward(reservationCtx, windowsAddr, local.Addr().String())
	reservationCancel()
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()
	conn, err := net.Dial("tcp", windowsAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("reverse")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "reverse" {
		t.Fatalf("got %q", buf)
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestReverseForwardIPv6(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	local, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer local.Close()
	go func() {
		conn, acceptErr := local.Accept()
		if acceptErr == nil {
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
		}
	}()
	probe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	windowsAddr := probe.Addr().String()
	_ = probe.Close()

	forward, err := client.ReverseForward(ctx, windowsAddr, local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()
	conn, err := net.Dial("tcp6", windowsAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("reverse-v6")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "reverse-v6" {
		t.Fatalf("got %q", buf)
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestReverseForwardRejectsOccupiedWindowsPort(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if _, err := client.ReverseForward(ctx, occupied.Addr().String(), "127.0.0.1:1"); err == nil {
		t.Fatal("expected Windows bind rejection")
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestReverseForwardCloseBeforeCommit(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	reservation, err := client.ReserveReverseForward(ctx, address, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestReverseUDPForwardRejectsOccupiedWindowsPort(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	occupied, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := client.ReverseDatagramForward(ctx, occupied.LocalAddr().String(), target.LocalAddr().String()); err == nil {
		t.Fatal("expected Windows UDP bind rejection")
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestServerReverseUDPOpenCloseOrdering(t *testing.T) {
	server := NewServer(&discardReadWriter{}, nil)
	server.ctx = context.Background()

	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	address := probe.LocalAddr().String()
	_ = probe.Close()

	server.handle(protocol.Frame{Type: protocol.TypeListenDatagramOpen, StreamID: 17, Payload: []byte(address)})
	server.handle(protocol.Frame{Type: protocol.TypeListenDatagramClose, StreamID: 17})

	server.mu.Lock()
	_, present := server.reverseDatagrams[17]
	server.mu.Unlock()
	if present {
		t.Fatal("reverse UDP association remained after ordered close")
	}
}

func TestReverseForwardReservationWaitsForCommit(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	go func() {
		conn, acceptErr := local.Accept()
		if acceptErr == nil {
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
		}
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	windowsAddr := probe.Addr().String()
	_ = probe.Close()

	reservation, err := client.ReserveReverseForward(ctx, windowsAddr, local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	if duplicate, bindErr := net.Listen("tcp", windowsAddr); bindErr == nil {
		_ = duplicate.Close()
		t.Fatal("reserved Windows address was not bound")
	}
	conn, err := net.Dial("tcp", windowsAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("commit")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 6)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("connection was accepted before commit")
	}
	if err := reservation.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "commit" {
		t.Fatalf("got %q", buf)
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestInitiatorIDsUseSeparateParity(t *testing.T) {
	client := NewClient(&discardReadWriter{})
	if id := client.nextID.Add(2); id%2 != 1 {
		t.Fatalf("client id %d is not odd", id)
	}
	server := NewServer(&discardReadWriter{}, nil)
	if id := server.nextStream.Add(2); id%2 != 0 {
		t.Fatalf("server id %d is not even", id)
	}
}

func TestClientServerUDPDatagram(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buffer := make([]byte, 128)
		count, source, readErr := echo.ReadFromUDP(buffer)
		if readErr == nil {
			_, _ = echo.WriteToUDP(buffer[:count], source)
		}
	}()
	packet, err := client.OpenPacketContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	_ = packet.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.WriteTo([]byte("datagram"), echo.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	count, source, err := packet.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != "datagram" {
		t.Fatalf("got %q", buffer[:count])
	}
	if source.String() != echo.LocalAddr().String() {
		t.Fatalf("source %s, want %s", source, echo.LocalAddr())
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestReverseUDPForward(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, nil)
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()

	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		buffer := make([]byte, 128)
		count, source, readErr := target.ReadFromUDP(buffer)
		if readErr == nil {
			_, _ = target.WriteToUDP(buffer[:count], source)
		}
	}()
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	windowsAddr := probe.LocalAddr().String()
	_ = probe.Close()
	forward, err := client.ReverseDatagramForward(ctx, windowsAddr, target.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()

	external, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	_ = external.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := external.WriteToUDP([]byte("reverse-udp"), mustUDPAddr(t, windowsAddr)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	count, _, err := external.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != "reverse-udp" {
		t.Fatalf("got %q", buffer[:count])
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestReverseUDPForwardBoundsSourceFlows(t *testing.T) {
	listener := newClientReverseDatagram(NewClient(&discardReadWriter{}), 1, mustUDPAddr(t, "127.0.0.1:9"), context.Background())
	for index := 0; index < maxReverseDatagramFlows; index++ {
		listener.flows[fmt.Sprintf("source-%d", index)] = nil
	}
	listener.handle(protocol.Frame{Type: protocol.TypeListenDatagramData, StreamID: 1, Payload: mustDatagramPayload(t, "127.0.0.1:9", []byte("drop"))})
	if len(listener.flows) != maxReverseDatagramFlows {
		t.Fatalf("flow count=%d, want cap %d", len(listener.flows), maxReverseDatagramFlows)
	}
}

func mustDatagramPayload(t *testing.T, endpoint string, data []byte) []byte {
	t.Helper()
	payload, err := protocol.EncodeDatagram(endpoint, data)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func mustUDPAddr(t *testing.T, value string) *net.UDPAddr {
	t.Helper()
	address, err := net.ResolveUDPAddr("udp", value)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func TestServerDatagramReadErrorRemovesAssociation(t *testing.T) {
	packet := &errorPacketConn{closed: make(chan struct{})}
	server := NewServer(&discardReadWriter{}, nil)
	server.ctx = context.Background()
	server.packetDial = func(context.Context) (net.PacketConn, error) { return packet, nil }
	server.openDatagram(2)
	select {
	case <-packet.closed:
	case <-time.After(time.Second):
		t.Fatal("datagram packet connection was not closed")
	}
	server.mu.Lock()
	_, present := server.datagrams[2]
	server.mu.Unlock()
	if present {
		t.Fatal("datagram association remained registered after read failure")
	}
}

func TestDatagramDecodeErrorsUseMatchingFrameType(t *testing.T) {
	tests := []struct {
		name string
		kind protocol.Type
		run  func(*Server) func()
	}{
		{name: "outbound", kind: protocol.TypeDatagramError, run: func(server *Server) func() {
			datagram := &serverDatagram{incoming: make(chan []byte, 1), done: make(chan struct{})}
			go server.writeDatagrams(2, datagram)
			datagram.incoming <- []byte{1}
			return func() { close(datagram.done) }
		}},
		{name: "reverse", kind: protocol.TypeListenDatagramError, run: func(server *Server) func() {
			datagram := &serverReverseDatagram{incoming: make(chan []byte, 1), done: make(chan struct{})}
			go server.writeReverseDatagrams(2, datagram)
			datagram.incoming <- []byte{1}
			return func() { close(datagram.done) }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := &frameCapture{done: make(chan struct{})}
			server := NewServer(capture, nil)
			stop := test.run(server)
			defer stop()
			select {
			case <-capture.done:
			case <-time.After(time.Second):
				t.Fatal("error frame was not written")
			}
			frame, err := protocol.Read(bytes.NewReader(capture.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if frame.Type != test.kind {
				t.Fatalf("frame type=%v, want %v", frame.Type, test.kind)
			}
		})
	}
}

func TestSlowStreamDoesNotBlockOtherStreams(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(serverSide, func(_ context.Context, target string) (net.Conn, error) {
		local, remote := net.Pipe()
		if target == "slow:1" {
			return local, nil
		}
		go func() { _, _ = io.Copy(remote, remote); _ = remote.Close() }()
		return local, nil
	})
	go func() { _ = server.Serve(ctx) }()
	client := NewClient(clientSide)
	go func() { _ = client.Run(ctx) }()
	slow, err := client.DialContext(ctx, "slow:1")
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	go func() { _, _ = slow.Write(make([]byte, protocol.InitialStreamWindow*2)) }()
	time.Sleep(20 * time.Millisecond)
	fastCtx, fastCancel := context.WithTimeout(ctx, time.Second)
	defer fastCancel()
	fast, err := client.DialContext(fastCtx, "fast:2")
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	_ = fast.SetDeadline(time.Now().Add(time.Second))
	if _, err := fast.Write([]byte("fast")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(fast, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "fast" {
		t.Fatalf("got %q", buffer)
	}
	_ = client.Close()
	_ = serverSide.Close()
	_ = clientSide.Close()
}

func TestSlowDatagramConsumerDropsInsteadOfBlocking(t *testing.T) {
	client := NewClient(&discardReadWriter{})
	packet := &clientPacketConn{client: client, id: 1, ready: make(chan error, 1), incoming: make(chan packetEvent, 1), done: make(chan struct{})}
	payload, err := protocol.EncodeDatagram("127.0.0.1:53", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	packet.handle(protocol.Frame{Type: protocol.TypeDatagramData, StreamID: 1, Payload: payload})
	done := make(chan struct{})
	go func() {
		packet.handle(protocol.Frame{Type: protocol.TypeDatagramData, StreamID: 1, Payload: payload})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full datagram queue blocked dispatch")
	}
}

func TestStreamEOFIsPersistentAfterHalfClose(t *testing.T) {
	stream := newClientStream(NewClient(&discardReadWriter{}), 1, "example:1")
	stream.handle(protocol.Frame{Type: protocol.TypeHalfClose, StreamID: 1})
	buffer := make([]byte, 1)
	if _, err := stream.Read(buffer); err != io.EOF {
		t.Fatalf("first read: %v", err)
	}
	if _, err := stream.Read(buffer); err != io.EOF {
		t.Fatalf("second read: %v", err)
	}
}

func TestHalfCloseDoesNotBlockFullReceiveQueue(t *testing.T) {
	stream := newClientStream(NewClient(&discardReadWriter{}), 1, "example:1")
	for index := 0; index < cap(stream.incoming); index++ {
		stream.incoming <- streamEvent{data: []byte("x")}
	}
	done := make(chan struct{})
	go func() {
		stream.handle(protocol.Frame{Type: protocol.TypeHalfClose})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("half-close handler blocked on a full data queue")
	}
	stream.stateMu.Lock()
	readEOF := stream.readEOF
	stream.stateMu.Unlock()
	if !readEOF {
		t.Fatal("half-close did not publish EOF state")
	}
	buffer := make([]byte, 1)
	for index := 0; index < cap(stream.incoming); index++ {
		if _, err := stream.Read(buffer); err != nil {
			t.Fatalf("read queued data %d: %v", index, err)
		}
	}
	if _, err := stream.Read(buffer); err != io.EOF {
		t.Fatalf("read after queued data: %v", err)
	}
}

func TestConcurrentReadsSerializeBufferedData(t *testing.T) {
	stream := newClientStream(NewClient(&discardReadWriter{}), 1, "example:1")
	stream.handle(protocol.Frame{Type: protocol.TypeData, StreamID: 1, Payload: []byte("abc")})
	stream.handle(protocol.Frame{Type: protocol.TypeData, StreamID: 1, Payload: []byte("def")})

	results := make(chan string, 2)
	for range 2 {
		go func() {
			buffer := make([]byte, 3)
			count, err := stream.Read(buffer)
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			results <- string(buffer[:count])
		}()
	}
	first, second := <-results, <-results
	if (first != "abc" || second != "def") && (first != "def" || second != "abc") {
		t.Fatalf("concurrent reads returned %q and %q", first, second)
	}
}

func TestZeroLengthReadReturnsImmediately(t *testing.T) {
	stream := newClientStream(NewClient(&discardReadWriter{}), 1, "example:1")
	result := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		n, err := stream.Read(nil)
		result <- struct {
			n   int
			err error
		}{n: n, err: err}
	}()
	select {
	case got := <-result:
		if got.n != 0 || got.err != nil {
			t.Fatalf("zero-length read returned n=%d err=%v", got.n, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("zero-length read blocked")
	}
}

func TestReadDeadlineUpdateWakesBlockedRead(t *testing.T) {
	stream := newClientStream(NewClient(&discardReadWriter{}), 1, "example:1")
	if err := stream.SetReadDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 1))
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := stream.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		var timeout osTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("read error=%v, want timeout", err)
		}
	case <-time.After(time.Second):
		_ = stream.Close()
		t.Fatal("blocked read did not observe updated deadline")
	}
}

func TestPacketReadDeadlineUpdateWakesBlockedRead(t *testing.T) {
	packet := &clientPacketConn{client: NewClient(&discardReadWriter{}), id: 1, ready: make(chan error, 1), incoming: make(chan packetEvent), done: make(chan struct{})}
	if err := packet.SetReadDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, err := packet.ReadFrom(make([]byte, 1))
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := packet.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		var timeout osTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("packet read error=%v, want timeout", err)
		}
	case <-time.After(time.Second):
		_ = packet.Close()
		t.Fatal("blocked packet read did not observe updated deadline")
	}
}

func TestWriteDeadlineUpdateWakesBlockedWrite(t *testing.T) {
	stream := newClientStream(NewClient(&discardReadWriter{}), 1, "example:1")
	stream.sendWindow.mu.Lock()
	stream.sendWindow.available = 0
	stream.sendWindow.mu.Unlock()
	if err := stream.SetWriteDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := stream.Write([]byte("x"))
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := stream.SetWriteDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		var timeout osTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("write error=%v, want timeout", err)
		}
	case <-time.After(time.Second):
		_ = stream.Close()
		t.Fatal("blocked write did not observe updated deadline")
	}
}

type discardReadWriter struct{}

func (*discardReadWriter) Read([]byte) (int, error)    { return 0, io.EOF }
func (*discardReadWriter) Write(p []byte) (int, error) { return len(p), nil }

type frameCapture struct {
	mu     sync.Mutex
	data   bytes.Buffer
	writes int
	done   chan struct{}
}

func (w *frameCapture) Read([]byte) (int, error) { return 0, io.EOF }
func (w *frameCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.data.Write(p)
	w.writes++
	if w.writes == 2 {
		close(w.done)
	}
	return len(p), nil
}
func (w *frameCapture) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.data.Bytes()...)
}

type blockingWriteReadWriter struct{ unblock chan struct{} }

func (*blockingWriteReadWriter) Read([]byte) (int, error) { return 0, io.EOF }
func (w *blockingWriteReadWriter) Write(p []byte) (int, error) {
	<-w.unblock
	return len(p), nil
}

type blockingReadCloser struct {
	unblock chan struct{}
	closed  chan struct{}
}

type errorPacketConn struct {
	closed chan struct{}
}

func (p *errorPacketConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, io.ErrUnexpectedEOF }
func (p *errorPacketConn) WriteTo([]byte, net.Addr) (int, error)  { return 0, io.ErrClosedPipe }
func (p *errorPacketConn) Close() error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return nil
}
func (p *errorPacketConn) LocalAddr() net.Addr              { return relayAddr("error") }
func (p *errorPacketConn) SetDeadline(time.Time) error      { return nil }
func (p *errorPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (p *errorPacketConn) SetWriteDeadline(time.Time) error { return nil }

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.unblock
	return 0, io.EOF
}
func (*blockingReadCloser) Write(p []byte) (int, error) { return len(p), nil }
func (r *blockingReadCloser) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	close(r.unblock)
	return nil
}
