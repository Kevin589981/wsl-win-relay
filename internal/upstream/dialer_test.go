package upstream

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHTTPConnectDialerPreservesTunnelData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		request, readErr := readRawHeaders(conn)
		if readErr != nil {
			serverErr <- readErr
			return
		}
		if !strings.HasPrefix(request, "CONNECT example.test:443 HTTP/1.1\r\n") {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, writeErr := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nproxy-data"); writeErr != nil {
			serverErr <- writeErr
			return
		}
		buffer := make([]byte, len("client-data"))
		if _, readErr := io.ReadFull(conn, buffer); readErr != nil {
			serverErr <- readErr
			return
		}
		_, writeErr := conn.Write(buffer)
		serverErr <- writeErr
	}()

	dialer, err := New("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	buffer := make([]byte, len("proxy-data"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "proxy-data" {
		t.Fatalf("got %q", buffer)
	}
	if _, err := io.WriteString(conn, "client-data"); err != nil {
		t.Fatal(err)
	}
	buffer = make([]byte, len("client-data"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "client-data" {
		t.Fatalf("echo %q", buffer)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5Dialer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		methods := make([]byte, 3)
		if _, err := io.ReadFull(conn, methods); err != nil {
			serverErr <- err
			return
		}
		if methods[0] != 5 || methods[1] != 1 || methods[2] != 0 {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			serverErr <- err
			return
		}
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			serverErr <- err
			return
		}
		if header[0] != 5 || header[1] != 1 || header[3] != 3 {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		nameLength := []byte{0}
		if _, err := io.ReadFull(conn, nameLength); err != nil {
			serverErr <- err
			return
		}
		name := make([]byte, int(nameLength[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			serverErr <- err
			return
		}
		port := make([]byte, 2)
		if _, err := io.ReadFull(conn, port); err != nil {
			serverErr <- err
			return
		}
		if string(name) != "example.test" || port[0] != 1 || port[1] != 187 {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1}); err != nil {
			serverErr <- err
			return
		}
		_, writeErr := io.Copy(conn, conn)
		serverErr <- writeErr
	}()

	dialer, err := New("socks5h://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "ping" {
		t.Fatalf("echo %q", buffer)
	}
	_ = conn.Close()
	if err := <-serverErr; err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatal(err)
	}
}

func TestSOCKS5PacketDialer(t *testing.T) {
	udpProxy, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udpProxy.Close()
	tcpProxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpProxy.Close()
	serverErr := make(chan error, 2)
	go func() {
		conn, acceptErr := tcpProxy.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		methods := make([]byte, 3)
		if _, err := io.ReadFull(conn, methods); err != nil {
			serverErr <- err
			return
		}
		if methods[0] != 5 || methods[1] != 1 || methods[2] != 0 {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			serverErr <- err
			return
		}
		request := make([]byte, 10)
		if _, err := io.ReadFull(conn, request); err != nil {
			serverErr <- err
			return
		}
		if request[0] != 5 || request[1] != 3 || request[3] != 1 {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		port := udpProxy.LocalAddr().(*net.UDPAddr).Port
		reply := []byte{5, 0, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)}
		_, writeErr := conn.Write(reply)
		serverErr <- writeErr
		if writeErr == nil {
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	go func() {
		buffer := make([]byte, 2048)
		count, source, readErr := udpProxy.ReadFromUDP(buffer)
		if readErr != nil {
			serverErr <- readErr
			return
		}
		if count < 4 || buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 || buffer[3] != 3 || buffer[4] != byte(len("example.test")) {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		portOffset := 5 + len("example.test")
		if string(buffer[5:portOffset]) != "example.test" || binary.BigEndian.Uint16(buffer[portOffset:portOffset+2]) != 5353 || string(buffer[portOffset+2:count]) != "ping" {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		response := append([]byte{0, 0, 0, 1, 8, 8, 8, 8, 0, 53}, []byte("pong")...)
		_, writeErr := udpProxy.WriteToUDP(response, source)
		serverErr <- writeErr
	}()

	dialer, err := New("socks5://" + tcpProxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	packet, err := dialer.OpenPacketContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	if _, err := packet.WriteTo([]byte("ping"), packetAddr("example.test:5353")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	_ = packet.SetReadDeadline(time.Now().Add(time.Second))
	count, source, err := packet.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != "pong" || source.String() != "8.8.8.8:53" {
		t.Fatalf("response %q from %s", buffer[:count], source)
	}
	packetImpl := packet.(*socks5PacketConn)
	if err := packet.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-packetImpl.done:
	case <-time.After(time.Second):
		t.Fatal("packet close did not signal cancellation watcher")
	}
	for i := 0; i < 2; i++ {
		if err := <-serverErr; err != nil && !strings.Contains(err.Error(), "closed") {
			t.Fatal(err)
		}
	}
}

func TestNewRejectsUnsupportedProxy(t *testing.T) {
	if _, err := New("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
	if _, err := New("http://127.0.0.1"); err == nil {
		t.Fatal("expected missing port error")
	}
}

func readRawHeaders(conn net.Conn) (string, error) {
	reader := bufio.NewReader(conn)
	var builder strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		builder.WriteString(line)
		if line == "\r\n" {
			return builder.String(), nil
		}
	}
}
