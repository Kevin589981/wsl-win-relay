package relay

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
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

	forward, err := client.ReverseForward(ctx, windowsAddr, local.Addr().String())
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
	packet := &clientPacketConn{client: client, id: 1, ready: make(chan error, 1), incoming: make(chan packetEvent, 1)}
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

type discardReadWriter struct{}

func (*discardReadWriter) Read([]byte) (int, error)    { return 0, io.EOF }
func (*discardReadWriter) Write(p []byte) (int, error) { return len(p), nil }
