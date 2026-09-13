package socks5

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

type echoDialer struct{ target chan string }

func (d *echoDialer) DialContext(context.Context, string) (net.Conn, error) {
	local, remote := net.Pipe()
	go func() { _, _ = io.Copy(remote, remote); _ = remote.Close() }()
	return local, nil
}

func (d *echoDialer) OpenPacketContext(context.Context) (net.PacketConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, err
	}
	return &resolvingPacketConn{UDPConn: conn}, nil
}

type resolvingPacketConn struct{ *net.UDPConn }

func (c *resolvingPacketConn) WriteTo(data []byte, address net.Addr) (int, error) {
	target, err := net.ResolveUDPAddr("udp", address.String())
	if err != nil {
		return 0, err
	}
	return c.WriteToUDP(data, target)
}

func TestServeConnTimesOutStalledHandshake(t *testing.T) {
	client, serverConn := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		done <- (&Server{Dialer: &echoDialer{}, HandshakeTimeout: 10 * time.Millisecond}).ServeConn(context.Background(), serverConn)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled SOCKS5 handshake unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stalled SOCKS5 handshake did not time out")
	}
}

func TestServeConnTimesOutWaitingForRelay(t *testing.T) {
	client, serverConn := net.Pipe()
	s := &Server{Dialer: blockingDialer{}, DialTimeout: 20 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- s.ServeConn(context.Background(), serverConn) }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	if _, err := client.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(client, method); err != nil {
		t.Fatal(err)
	}
	request := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80}
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != replyHostUnreachable {
		t.Fatalf("reply: %v", reply)
	}
	_ = client.Close()
	if err := <-done; err == nil {
		t.Fatal("expected relay timeout error")
	}
}

type blockingDialer struct{}

func (blockingDialer) DialContext(ctx context.Context, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestServeConnConnectsDomainAndProxies(t *testing.T) {
	client, serverConn := net.Pipe()
	s := &Server{Dialer: &echoDialer{}}
	done := make(chan error, 1)
	go func() { done <- s.ServeConn(context.Background(), serverConn) }()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(client, method); err != nil {
		t.Fatal(err)
	}
	if method[0] != 5 || method[1] != 0 {
		t.Fatalf("method response: %v", method)
	}
	request := []byte{5, 1, 0, 3, 11}
	request = append(request, []byte("example.com")...)
	request = append(request, 0, 80)
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != replySucceeded {
		t.Fatalf("reply: %v", reply)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("got %q", buf)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestNegotiationRejectsAuthentication(t *testing.T) {
	client, serverConn := net.Pipe()
	s := &Server{Dialer: &echoDialer{}}
	done := make(chan error, 1)
	go func() { done <- s.ServeConn(context.Background(), serverConn) }()
	if _, err := client.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if response[1] != 0xff {
		t.Fatalf("got %v", response)
	}
	client.Close()
	<-done
}

func TestUDPAssociateProxiesDatagram(t *testing.T) {
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

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()
	server := &Server{Listener: tcpListener, Dialer: &echoDialer{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	control, err := net.Dial("tcp", tcpListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := control.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(control, method); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Write([]byte{5, commandUDPAssociate, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(control, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != replySucceeded || reply[3] != 1 {
		t.Fatalf("reply: %v", reply)
	}
	proxyAddr := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:10]))}
	udpClient, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()
	_ = udpClient.SetDeadline(time.Now().Add(2 * time.Second))
	encodedTarget, err := encodeAddress(echo.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	packet := append([]byte{0, 0, 0}, encodedTarget...)
	packet = append(packet, []byte("udp-ping")...)
	if _, err := udpClient.WriteToUDP(packet, proxyAddr); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 256)
	count, _, err := udpClient.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	source, data, err := parseUDPRequest(buffer[:count])
	if err != nil {
		t.Fatal(err)
	}
	if source != echo.LocalAddr().String() || string(data) != "udp-ping" {
		t.Fatalf("source=%s data=%q", source, data)
	}
}

func TestParseUDPRequestRejectsFragments(t *testing.T) {
	if _, _, err := parseUDPRequest([]byte{0, 0, 1, 1, 127, 0, 0, 1, 0, 53}); err == nil {
		t.Fatal("expected fragment rejection")
	}
}

func TestUDPAssociateClosesAfterIdleTimeout(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()
	server := &Server{Listener: tcpListener, Dialer: &echoDialer{}, UDPAssociateIdleTimeout: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	control, err := net.Dial("tcp", tcpListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if _, err := control.Write([]byte{5, 1, 0, 5, commandUDPAssociate, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(control, method); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(control, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != replySucceeded {
		t.Fatalf("reply: %v", reply)
	}
	_ = control.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if count, err := control.Read(buffer); err == nil {
		t.Fatalf("expected idle association to close (read %d bytes: %v)", count, buffer[:count])
	}
}
