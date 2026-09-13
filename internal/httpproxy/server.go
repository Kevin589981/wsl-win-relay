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
	"strings"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/netserve"
)

type Dialer interface {
	DialContext(context.Context, string) (net.Conn, error)
}

type Server struct {
	Listener         net.Listener
	Dialer           Dialer
	Logger           *log.Logger
	DialTimeout      time.Duration
	HandshakeTimeout time.Duration
}

const (
	defaultHandshakeTimeout = 15 * time.Second
	maxConnectRequestBytes  = 64 << 10
)

func (s *Server) Serve(ctx context.Context) error {
	if s.Listener == nil || s.Dialer == nil {
		return errors.New("listener and dialer are required")
	}
	if s.Logger == nil {
		s.Logger = log.New(io.Discard, "", 0)
	}
	return netserve.Serve(ctx, s.Listener, func(serveCtx context.Context, conn net.Conn) {
		if err := s.ServeConn(serveCtx, conn); err != nil {
			s.Logger.Printf("HTTP proxy connection: %v", err)
		}
	})
}

func (s *Server) ServeConn(ctx context.Context, client net.Conn) error {
	defer client.Close()
	timeout := s.HandshakeTimeout
	if timeout <= 0 {
		timeout = defaultHandshakeTimeout
	}
	if err := client.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	limited := &handshakeReader{reader: client, remaining: maxConnectRequestBytes, bounded: true}
	reader := bufio.NewReader(limited)
	request, err := http.ReadRequest(reader)
	if err != nil {
		writeError(client, http.StatusBadRequest)
		return err
	}
	limited.bounded = false
	if err := client.SetDeadline(time.Time{}); err != nil {
		return err
	}
	if request.Body != nil {
		defer request.Body.Close()
	}
	if request.Method != http.MethodConnect {
		return s.forwardHTTP(ctx, client, request)
	}
	target := request.Host
	if target == "" {
		target = request.RequestURI
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		writeError(client, http.StatusBadRequest)
		return fmt.Errorf("CONNECT target must be host:port: %w", err)
	}
	dialCtx, cancel := s.dialContext(ctx)
	remote, err := s.Dialer.DialContext(dialCtx, target)
	cancel()
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

func (s *Server) forwardHTTP(ctx context.Context, client net.Conn, request *http.Request) error {
	if request.URL == nil || !request.URL.IsAbs() || !strings.EqualFold(request.URL.Scheme, "http") {
		writeError(client, http.StatusBadRequest)
		return errors.New("plain HTTP proxy requests require an absolute http URL")
	}
	host := request.URL.Hostname()
	if host == "" {
		writeError(client, http.StatusBadRequest)
		return errors.New("plain HTTP proxy request has no target host")
	}
	port := request.URL.Port()
	if port == "" {
		port = "80"
	}
	target := net.JoinHostPort(host, port)
	dialCtx, cancel := s.dialContext(ctx)
	remote, err := s.Dialer.DialContext(dialCtx, target)
	cancel()
	if err != nil {
		writeError(client, http.StatusBadGateway)
		return err
	}
	defer remote.Close()

	// Forward one request per connection. Removing proxy-only headers and
	// forcing close avoids forwarding hop-by-hop state into the origin server.
	request.RequestURI = ""
	request.URL.Scheme = ""
	request.URL.Host = ""
	request.Header.Del("Proxy-Connection")
	request.Header.Del("Proxy-Authorization")
	request.Header.Del("Connection")
	request.Header.Set("Connection", "close")
	request.Close = true
	if err := request.Write(remote); err != nil {
		return err
	}
	if cw, ok := remote.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_, err = io.Copy(client, remote)
	return err
}

type handshakeReader struct {
	reader    io.Reader
	remaining int64
	bounded   bool
}

func (r *handshakeReader) Read(p []byte) (int, error) {
	if !r.bounded {
		return r.reader.Read(p)
	}
	if r.remaining <= 0 {
		return 0, errors.New("HTTP CONNECT request exceeds limit")
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (s *Server) dialContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.DialTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.DialTimeout)
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
