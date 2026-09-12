package socks5

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const defaultUDPAssociateIdleTimeout = 5 * time.Minute

type Dialer interface {
	DialContext(context.Context, string) (net.Conn, error)
}

func (s *Server) serveUDPAssociate(ctx context.Context, control net.Conn, packetDialer PacketDialer) error {
	openCtx, cancel := s.dialContext(ctx)
	relayPacket, err := packetDialer.OpenPacketContext(openCtx)
	cancel()
	if err != nil {
		_ = writeReply(control, mapDialError(err), nil)
		return err
	}
	return s.serveUDPAssociateWithPacket(ctx, control, relayPacket)
}

func (s *Server) serveUDPAssociateWithPacket(ctx context.Context, control net.Conn, relayPacket net.PacketConn) error {
	defer relayPacket.Close()
	local, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		_ = writeReply(control, replyGeneralFailure, nil)
		return err
	}
	defer local.Close()
	if err := writeReply(control, replySucceeded, local.LocalAddr()); err != nil {
		return err
	}

	associationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 3)
	idleTimeout := s.UDPAssociateIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultUDPAssociateIdleTimeout
	}
	activity := make(chan struct{}, 1)
	go func() {
		timer := time.NewTimer(idleTimeout)
		defer timer.Stop()
		for {
			select {
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idleTimeout)
			case <-timer.C:
				cancel()
				return
			case <-associationCtx.Done():
				return
			}
		}
	}()
	touch := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
	var clientMu sync.RWMutex
	var udpClient *net.UDPAddr
	go func() { _, copyErr := io.Copy(io.Discard, control); errCh <- copyErr }()
	go func() {
		buffer := make([]byte, 65535)
		for {
			count, source, readErr := local.ReadFromUDP(buffer)
			if readErr != nil {
				errCh <- readErr
				return
			}
			if !sameClientIP(control.RemoteAddr(), source) {
				continue
			}
			touch()
			target, data, parseErr := parseUDPRequest(buffer[:count])
			if parseErr != nil {
				s.Logger.Printf("UDP packet: %v", parseErr)
				continue
			}
			clientMu.Lock()
			udpClient = source
			clientMu.Unlock()
			if _, writeErr := relayPacket.WriteTo(data, stringAddr(target)); writeErr != nil {
				errCh <- writeErr
				return
			}
		}
	}()
	go func() {
		buffer := make([]byte, 65535)
		for {
			count, source, readErr := relayPacket.ReadFrom(buffer)
			if readErr != nil {
				errCh <- readErr
				return
			}
			touch()
			packet, encodeErr := encodeUDPResponse(source.String(), buffer[:count])
			if encodeErr != nil {
				continue
			}
			clientMu.RLock()
			destination := udpClient
			clientMu.RUnlock()
			if destination == nil {
				continue
			}
			if _, writeErr := local.WriteToUDP(packet, destination); writeErr != nil {
				errCh <- writeErr
				return
			}
		}
	}()

	select {
	case err := <-errCh:
		cancel()
		_ = local.Close()
		_ = relayPacket.Close()
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-associationCtx.Done():
		return associationCtx.Err()
	}
}

func sameClientIP(control net.Addr, udp *net.UDPAddr) bool {
	host, _, err := net.SplitHostPort(control.String())
	if err != nil {
		return true
	}
	ip := net.ParseIP(host)
	return ip == nil || ip.Equal(udp.IP)
}

func parseUDPRequest(packet []byte) (string, []byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 {
		return "", nil, errors.New("invalid UDP reserved field")
	}
	if packet[2] != 0 {
		return "", nil, errors.New("fragmented UDP packets are unsupported")
	}
	reader := bytes.NewReader(packet[4:])
	target, err := readAddress(reader, packet[3])
	if err != nil {
		return "", nil, err
	}
	consumed := len(packet[4:]) - reader.Len()
	return target, packet[4+consumed:], nil
}

func encodeUDPResponse(source string, data []byte) ([]byte, error) {
	address, err := encodeAddress(source)
	if err != nil {
		return nil, err
	}
	packet := make([]byte, 0, 3+len(address)+len(data))
	packet = append(packet, 0, 0, 0)
	packet = append(packet, address...)
	packet = append(packet, data...)
	return packet, nil
}

func encodeAddress(address string) ([]byte, error) {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil {
		return nil, err
	}
	var encoded []byte
	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		encoded = append([]byte{1}, ip4...)
	} else if ip6 := ip.To16(); ip6 != nil {
		encoded = append([]byte{4}, ip6...)
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, errors.New("invalid domain length")
		}
		encoded = append([]byte{3, byte(len(host))}, host...)
	}
	return append(encoded, byte(port>>8), byte(port)), nil
}

type stringAddr string

func (a stringAddr) Network() string { return "udp" }
func (a stringAddr) String() string  { return string(a) }

type PacketDialer interface {
	OpenPacketContext(context.Context) (net.PacketConn, error)
}

type Server struct {
	Listener                net.Listener
	Dialer                  Dialer
	Logger                  *log.Logger
	UDPAssociateIdleTimeout time.Duration
	DialTimeout             time.Duration
}

func (s *Server) Serve(ctx context.Context) error {
	if s.Listener == nil {
		return errors.New("listener is required")
	}
	if s.Dialer == nil {
		return errors.New("dialer is required")
	}
	if s.Logger == nil {
		s.Logger = log.New(io.Discard, "", 0)
	}
	go func() { <-ctx.Done(); _ = s.Listener.Close() }()
	for {
		conn, err := s.Listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go func() {
			if err := s.ServeConn(ctx, conn); err != nil {
				s.Logger.Printf("connection: %v", err)
			}
		}()
	}
}

func (s *Server) ServeConn(ctx context.Context, client net.Conn) error {
	defer client.Close()
	if err := negotiate(client); err != nil {
		return err
	}
	request, err := readRequest(client)
	if err != nil {
		_ = writeReply(client, replyGeneralFailure, nil)
		return err
	}
	if request.command == commandUDPAssociate {
		packetDialer, ok := s.Dialer.(PacketDialer)
		if !ok {
			_ = writeReply(client, replyCommandNotSupported, nil)
			return errors.New("UDP relay is unavailable")
		}
		return s.serveUDPAssociate(ctx, client, packetDialer)
	}
	if request.command != commandConnect {
		_ = writeReply(client, replyCommandNotSupported, nil)
		return errors.New("unsupported SOCKS command")
	}
	dialCtx, cancel := s.dialContext(ctx)
	remote, err := s.Dialer.DialContext(dialCtx, request.target)
	cancel()
	if err != nil {
		_ = writeReply(client, mapDialError(err), nil)
		return err
	}
	defer remote.Close()
	if err := writeReply(client, replySucceeded, remote.LocalAddr()); err != nil {
		return err
	}
	return proxy(client, remote)
}

func (s *Server) dialContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.DialTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.DialTimeout)
}

func negotiate(conn net.Conn) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	if header[0] != 5 {
		return errors.New("unsupported SOCKS version")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == 0 {
			_, err := conn.Write([]byte{5, 0})
			return err
		}
	}
	_, err := conn.Write([]byte{5, 0xff})
	if err == nil {
		err = errors.New("no supported SOCKS authentication method")
	}
	return err
}

const (
	commandConnect      = 1
	commandUDPAssociate = 3
)

type request struct {
	command byte
	target  string
}

func readRequest(conn net.Conn) (request, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return request{}, err
	}
	if header[0] != 5 {
		return request{}, errors.New("unsupported SOCKS version")
	}
	target, err := readAddress(conn, header[3])
	if err != nil {
		return request{}, err
	}
	return request{command: header[1], target: target}, nil
}

func readAddress(reader io.Reader, addressType byte) (string, error) {
	var host string
	switch addressType {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(reader, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 3:
		b := make([]byte, 1)
		if _, err := io.ReadFull(reader, b); err != nil {
			return "", err
		}
		if b[0] == 0 {
			return "", errors.New("empty domain")
		}
		name := make([]byte, int(b[0]))
		if _, err := io.ReadFull(reader, name); err != nil {
			return "", err
		}
		host = string(name)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(reader, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	default:
		return "", errors.New("unsupported address type")
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))), nil
}

const (
	replySucceeded            = 0
	replyGeneralFailure       = 1
	replyConnectionNotAllowed = 2
	replyNetworkUnreachable   = 3
	replyHostUnreachable      = 4
	replyConnectionRefused    = 5
	replyCommandNotSupported  = 7
)

func writeReply(conn net.Conn, code byte, addr net.Addr) error {
	var host string
	var port int
	if addr != nil {
		host, _, _ = net.SplitHostPort(addr.String())
		if _, p, err := net.SplitHostPort(addr.String()); err == nil {
			port, _ = strconv.Atoi(p)
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		ip = net.IPv4zero
	}
	if ip4 := ip.To4(); ip4 != nil {
		buf := []byte{5, code, 0, 1, ip4[0], ip4[1], ip4[2], ip4[3], byte(port >> 8), byte(port)}
		_, err := conn.Write(buf)
		return err
	}
	b := ip.To16()
	if b == nil {
		b = make([]byte, 16)
	}
	buf := append([]byte{5, code, 0, 4}, b...)
	buf = append(buf, byte(port>>8), byte(port))
	_, err := conn.Write(buf)
	return err
}

func mapDialError(err error) byte {
	if errors.Is(err, context.Canceled) {
		return replyGeneralFailure
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return replyHostUnreachable
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return replyConnectionRefused
	}
	return replyGeneralFailure
}

func proxy(a, b net.Conn) error {
	errCh := make(chan error, 2)
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		// Preserve the opposite direction after one side sends FIN. TLS and
		// HTTP clients commonly half-close their request before reading fully.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			// Some test and non-TCP connections have no half-close primitive.
			// Closing both sides is the only way to unblock the peer.
			_ = dst.Close()
			_ = src.Close()
		}
		errCh <- err
	}
	go copyOne(a, b)
	go copyOne(b, a)
	first := <-errCh
	<-errCh
	_ = a.Close()
	_ = b.Close()
	return first
}
