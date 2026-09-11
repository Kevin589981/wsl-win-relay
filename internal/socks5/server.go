package socks5

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"syscall"
)

type Dialer interface {
	DialContext(context.Context, string) (net.Conn, error)
}

type Server struct {
	Listener net.Listener
	Dialer   Dialer
	Logger   *log.Logger
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
	target, err := readRequest(client)
	if err != nil {
		_ = writeReply(client, replyGeneralFailure, nil)
		return err
	}
	remote, err := s.Dialer.DialContext(ctx, target)
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

func readRequest(conn net.Conn) (string, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", err
	}
	if header[0] != 5 {
		return "", errors.New("unsupported SOCKS version")
	}
	if header[1] != 1 {
		return "", errors.New("only CONNECT is supported")
	}
	var host string
	switch header[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 3:
		b := make([]byte, 1)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		if b[0] == 0 {
			return "", errors.New("empty domain")
		}
		name := make([]byte, int(b[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", err
		}
		host = string(name)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	default:
		return "", errors.New("unsupported address type")
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
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
