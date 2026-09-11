package upstream

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const maxProxyHeader = 64 << 10

// Dialer optionally sends TCP connections through an upstream HTTP CONNECT or
// SOCKS5 proxy. An empty URL preserves direct WinSock dialing.
type Dialer struct {
	proxy  *url.URL
	dialer net.Dialer
}

func New(raw string) (*Dialer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &Dialer{}, nil
	}
	proxy, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse upstream proxy: %w", err)
	}
	switch strings.ToLower(proxy.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported upstream proxy scheme %q", proxy.Scheme)
	}
	if proxy.Host == "" {
		return nil, errors.New("upstream proxy URL must include a host")
	}
	if _, _, err := net.SplitHostPort(proxy.Host); err != nil {
		return nil, fmt.Errorf("upstream proxy must include host:port: %w", err)
	}
	return &Dialer{proxy: proxy}, nil
}

func (d *Dialer) DialContext(ctx context.Context, target string) (net.Conn, error) {
	if d == nil || d.proxy == nil {
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, fmt.Errorf("invalid target %q: %w", target, err)
	}
	conn, err := d.dialer.DialContext(ctx, "tcp", d.proxy.Host)
	if err != nil {
		return nil, err
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-finished:
		}
	}()
	var setupErr error
	switch strings.ToLower(d.proxy.Scheme) {
	case "http", "https":
		if strings.EqualFold(d.proxy.Scheme, "https") {
			host, _, _ := net.SplitHostPort(d.proxy.Host)
			conn = tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		}
		setupErr = httpConnect(conn, target, d.proxy.User)
	case "socks5", "socks5h":
		setupErr = socks5Connect(conn, target, d.proxy.User)
	}
	close(finished)
	if setupErr != nil {
		_ = conn.Close()
		return nil, setupErr
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func httpConnect(conn net.Conn, target string, user *url.Userinfo) error {
	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nProxy-Connection: Keep-Alive\r\n"
	if user != nil {
		password, _ := user.Password()
		credentials := user.Username() + ":" + password
		request += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(credentials)) + "\r\n"
	}
	request += "\r\n"
	if err := writeAll(conn, []byte(request)); err != nil {
		return fmt.Errorf("upstream HTTP CONNECT request: %w", err)
	}
	used := 0
	readLine := func() (string, error) {
		line := make([]byte, 0, 128)
		for {
			if used >= maxProxyHeader {
				return "", errors.New("upstream HTTP CONNECT headers exceed limit")
			}
			var one [1]byte
			count, err := conn.Read(one[:])
			if count == 0 && err == nil {
				return "", io.ErrNoProgress
			}
			used += count
			if err != nil {
				return "", err
			}
			line = append(line, one[0])
			if one[0] == '\n' {
				return string(line), nil
			}
		}
	}
	status, err := readLine()
	if err != nil {
		return fmt.Errorf("read upstream HTTP CONNECT response: %w", err)
	}
	fields := strings.Fields(status)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return fmt.Errorf("invalid upstream HTTP CONNECT status %q", strings.TrimSpace(status))
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return fmt.Errorf("invalid upstream HTTP CONNECT code: %w", err)
	}
	for {
		line, readErr := readLine()
		if readErr != nil {
			return fmt.Errorf("read upstream HTTP CONNECT headers: %w", readErr)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("upstream HTTP CONNECT rejected target with status %d", code)
	}
	return nil
}

func socks5Connect(conn net.Conn, target string, user *url.Userinfo) error {
	methods := []byte{0}
	if user != nil {
		methods = append(methods, 2)
	}
	if err := writeAll(conn, append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return fmt.Errorf("upstream SOCKS5 negotiation: %w", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		return fmt.Errorf("read upstream SOCKS5 method: %w", err)
	}
	if methodReply[0] != 5 || methodReply[1] == 0xff {
		return errors.New("upstream SOCKS5 has no usable authentication method")
	}
	if methodReply[1] == 2 {
		if user == nil {
			return errors.New("upstream SOCKS5 requested credentials unexpectedly")
		}
		password, _ := user.Password()
		if len(user.Username()) > 255 || len(password) > 255 {
			return errors.New("upstream SOCKS5 credentials are too long")
		}
		auth := []byte{1, byte(len(user.Username()))}
		auth = append(auth, user.Username()...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if err := writeAll(conn, auth); err != nil {
			return fmt.Errorf("upstream SOCKS5 authentication: %w", err)
		}
		authReply := make([]byte, 2)
		if _, err := io.ReadFull(conn, authReply); err != nil {
			return fmt.Errorf("read upstream SOCKS5 authentication: %w", err)
		}
		if authReply[1] != 0 {
			return errors.New("upstream SOCKS5 authentication failed")
		}
	} else if methodReply[1] != 0 {
		return fmt.Errorf("unsupported upstream SOCKS5 method %d", methodReply[1])
	}
	address, err := encodeTarget(target)
	if err != nil {
		return err
	}
	request := append([]byte{5, 1, 0}, address...)
	if err := writeAll(conn, request); err != nil {
		return fmt.Errorf("upstream SOCKS5 connect request: %w", err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return fmt.Errorf("read upstream SOCKS5 connect response: %w", err)
	}
	if header[0] != 5 || header[1] != 0 {
		return fmt.Errorf("upstream SOCKS5 rejected target with code %d", header[1])
	}
	if err := discardAddress(conn, header[3]); err != nil {
		return fmt.Errorf("read upstream SOCKS5 bind address: %w", err)
	}
	return nil
}

func encodeTarget(target string) ([]byte, error) {
	host, rawPort, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("invalid target %q: %w", target, err)
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("invalid target port %q", rawPort)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return append([]byte{1}, append(ip4, byte(port>>8), byte(port))...), nil
		}
		if ip6 := ip.To16(); ip6 != nil {
			return append([]byte{4}, append(ip6, byte(port>>8), byte(port))...), nil
		}
	}
	if len(host) == 0 || len(host) > 255 {
		return nil, errors.New("invalid target domain length")
	}
	return append([]byte{3, byte(len(host))}, append([]byte(host), byte(port>>8), byte(port))...), nil
}

func discardAddress(reader io.Reader, addressType byte) error {
	var length int
	switch addressType {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		nameLength := []byte{0}
		if _, err := io.ReadFull(reader, nameLength); err != nil {
			return err
		}
		length = int(nameLength[0])
	default:
		return fmt.Errorf("unsupported address type %d", addressType)
	}
	address := make([]byte, length+2)
	_, err := io.ReadFull(reader, address)
	return err
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		if err != nil {
			return err
		}
		if count <= 0 || count > len(data) {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}
