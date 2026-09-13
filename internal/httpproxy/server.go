package httpproxy

import (
	"bufio"
	"bytes"
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
	MaxConnections   int
}

const (
	defaultHandshakeTimeout = 15 * time.Second
	maxRequestHeaderBytes   = 64 << 10
)

func (s *Server) Serve(ctx context.Context) error {
	if s.Listener == nil || s.Dialer == nil {
		return errors.New("listener and dialer are required")
	}
	if s.Logger == nil {
		s.Logger = log.New(io.Discard, "", 0)
	}
	maxConnections := s.MaxConnections
	if maxConnections <= 0 {
		maxConnections = netserve.DefaultMaxConnections
	}
	return netserve.ServeWithLimit(ctx, s.Listener, maxConnections, func(serveCtx context.Context, conn net.Conn) {
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
	stream := &requestStream{reader: client}
	request, reader, err := readRequest(stream)
	if err != nil {
		writeError(client, http.StatusBadRequest)
		return err
	}
	if err := client.SetDeadline(time.Time{}); err != nil {
		return err
	}
	if request.Method != http.MethodConnect {
		return s.serveHTTP(ctx, client, stream, reader, request, timeout)
	}
	if err := captureBuffered(reader, stream); err != nil {
		return err
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
	return bridge(&bufferedConn{Conn: client, reader: stream}, remote)
}

func (s *Server) serveHTTP(ctx context.Context, client net.Conn, stream *requestStream, reader *bufio.Reader, request *http.Request, timeout time.Duration) error {
	for {
		if err := s.forwardHTTP(ctx, client, request); err != nil {
			return err
		}
		if err := captureBuffered(reader, stream); err != nil {
			return err
		}
		if request.Close {
			return nil
		}
		if err := client.SetDeadline(time.Now().Add(timeout)); err != nil {
			if isClientDisconnect(err) {
				return nil
			}
			return err
		}
		next, nextReader, err := readRequest(stream)
		if isClientDisconnect(err) {
			return nil
		}
		if err != nil {
			writeError(client, http.StatusBadRequest)
			return err
		}
		if err := client.SetDeadline(time.Time{}); err != nil {
			return err
		}
		if next.Method == http.MethodConnect {
			writeError(client, http.StatusBadRequest)
			return errors.New("CONNECT cannot follow a plain HTTP proxy request")
		}
		reader = nextReader
		request = next
	}
}

func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	return err == io.EOF || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "closed pipe")
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
	if request.Body != nil {
		defer request.Body.Close()
	}

	// Each request uses a fresh origin connection. Removing proxy-only headers
	// and forcing close avoids forwarding hop-by-hop state into the origin.
	request.RequestURI = ""
	request.URL.Scheme = ""
	request.URL.Host = ""
	stripHopByHopHeaders(request.Header)
	request.Header.Set("Connection", "close")
	if err := request.Write(remote); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(remote), request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	stripHopByHopHeaders(response.Header)
	response.Close = request.Close
	if response.Close {
		response.Header.Set("Connection", "close")
	}
	return response.Write(client)
}

func stripHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if name := strings.TrimSpace(token); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"TE",
		"Trailer",
		"Upgrade",
	} {
		header.Del(name)
	}
}

// requestStream preserves bytes that http.ReadRequest buffered beyond the
// current request body, allowing each request to have its own bounded parser.
// It is single-consumer state owned by one HTTP proxy connection.
type requestStream struct {
	reader  io.Reader
	pending []byte
}

func (s *requestStream) Read(p []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	return s.reader.Read(p)
}

func (s *requestStream) prepend(data []byte) {
	if len(data) == 0 {
		return
	}
	pending := make([]byte, len(data)+len(s.pending))
	copy(pending, data)
	copy(pending[len(data):], s.pending)
	s.pending = pending
}

type requestHeaderReader struct {
	stream      *requestStream
	bytes       int
	complete    bool
	window      [4]byte
	windowBytes int
}

func (r *requestHeaderReader) Read(p []byte) (int, error) {
	if !r.complete && r.bytes >= maxRequestHeaderBytes {
		return 0, errors.New("HTTP proxy request headers exceed limit")
	}
	if !r.complete {
		remaining := maxRequestHeaderBytes - r.bytes
		if len(p) > remaining {
			p = p[:remaining]
		}
	}
	n, err := r.stream.Read(p)
	for _, value := range p[:n] {
		if r.complete {
			break
		}
		r.bytes++
		if r.windowBytes < len(r.window) {
			r.window[r.windowBytes] = value
			r.windowBytes++
		} else {
			copy(r.window[:], r.window[1:])
			r.window[len(r.window)-1] = value
		}
		if r.windowBytes == len(r.window) && bytes.Equal(r.window[:], []byte("\r\n\r\n")) {
			r.complete = true
		}
	}
	return n, err
}

func readRequest(stream *requestStream) (*http.Request, *bufio.Reader, error) {
	headerReader := &requestHeaderReader{stream: stream}
	reader := bufio.NewReader(headerReader)
	request, err := http.ReadRequest(reader)
	return request, reader, err
}

func captureBuffered(reader *bufio.Reader, stream *requestStream) error {
	buffered := reader.Buffered()
	if buffered == 0 {
		return nil
	}
	data, err := reader.Peek(buffered)
	if err != nil {
		return err
	}
	stream.prepend(append([]byte(nil), data...))
	return nil
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
	reader io.Reader
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
