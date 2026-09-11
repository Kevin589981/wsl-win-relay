package httpproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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
	if s.Listener == nil || s.Dialer == nil {
		return errors.New("listener and dialer are required")
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
				s.Logger.Printf("HTTP proxy connection: %v", err)
			}
		}()
	}
}

func (s *Server) ServeConn(ctx context.Context, client net.Conn) error {
	defer client.Close()
	reader := bufio.NewReader(client)
	request, err := http.ReadRequest(reader)
	if err != nil {
		writeError(client, http.StatusBadRequest)
		return err
	}
	if request.Body != nil {
		_ = request.Body.Close()
	}
	if request.Method != http.MethodConnect {
		writeError(client, http.StatusMethodNotAllowed)
		return errors.New("only CONNECT is supported")
	}
	target := request.Host
	if target == "" {
		target = request.RequestURI
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		writeError(client, http.StatusBadRequest)
		return fmt.Errorf("CONNECT target must be host:port: %w", err)
	}
	remote, err := s.Dialer.DialContext(ctx, target)
	if err != nil {
		writeError(client, http.StatusBadGateway)
		return err
	}
	defer remote.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return err
	}
	return bridge(&bufferedConn{Conn: client, reader: reader}, remote)
}

func writeError(conn net.Conn, status int) {
	text := http.StatusText(status)
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nConnection: close\r\nContent-Length: 0\r\n\r\n", status, text)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func bridge(a, b net.Conn) error {
	errCh := make(chan error, 2)
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
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
