package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxProxyHeader = 64 << 10

const maxSOCKS5UDPPayload = 65535

// Dialer optionally sends TCP connections and UDP datagrams through an
// upstream HTTP CONNECT or SOCKS5 proxy. An empty URL preserves direct WinSock
// dialing.
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
	if strings.EqualFold(d.proxy.Scheme, "socks5") {
		resolved, resolveErr := resolveTarget(ctx, target)
		if resolveErr != nil {
			return nil, resolveErr
		}
		target = resolved
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

// OpenPacketContext opens a UDP PacketConn. SOCKS5 upstreams use UDP
// ASSOCIATE; HTTP upstreams do not define a UDP tunnel and therefore retain
// direct native UDP egress.
func (d *Dialer) OpenPacketContext(ctx context.Context) (net.PacketConn, error) {
	if d == nil || d.proxy == nil || strings.EqualFold(d.proxy.Scheme, "http") || strings.EqualFold(d.proxy.Scheme, "https") {
		return net.ListenUDP("udp", nil)
	}
	control, err := d.dialer.DialContext(ctx, "tcp", d.proxy.Host)
	if err != nil {
		return nil, err
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = control.Close()
		case <-finished:
		}
	}()
	if err := socks5Handshake(control, d.proxy.User); err != nil {
		close(finished)
		_ = control.Close()
		return nil, err
	}
	request := []byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}
	if err := writeAll(control, request); err != nil {
		close(finished)
		_ = control.Close()
		return nil, fmt.Errorf("upstream SOCKS5 UDP associate request: %w", err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(control, header); err != nil {
		close(finished)
		_ = control.Close()
		return nil, fmt.Errorf("read upstream SOCKS5 UDP associate response: %w", err)
	}
	if header[0] != 5 || header[1] != 0 {
		close(finished)
		_ = control.Close()
		return nil, fmt.Errorf("upstream SOCKS5 rejected UDP associate with code %d", header[1])
	}
	bind, err := readSocksAddress(control, header[3])
	if err != nil {
		close(finished)
		_ = control.Close()
		return nil, fmt.Errorf("read upstream SOCKS5 UDP relay address: %w", err)
	}
	relayHost, _, err := net.SplitHostPort(bind)
	if err != nil {
		close(finished)
		_ = control.Close()
		return nil, err
	}
	if relayHost == "0.0.0.0" || relayHost == "::" || relayHost == "" {
		relayHost, _, _ = net.SplitHostPort(d.proxy.Host)
	}
	_, relayPort, _ := net.SplitHostPort(bind)
	relayAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(relayHost, relayPort))
	if err != nil {
		close(finished)
		_ = control.Close()
		return nil, fmt.Errorf("resolve upstream SOCKS5 UDP relay address: %w", err)
	}
	udp, err := net.ListenUDP("udp", nil)
	if err != nil {
		close(finished)
		_ = control.Close()
		return nil, err
	}
	packet := &socks5PacketConn{control: control, udp: udp, relay: relayAddr, resolveDomains: strings.EqualFold(d.proxy.Scheme, "socks5"), done: make(chan struct{})}
	close(finished)
	go func() {
		select {
		case <-ctx.Done():
			_ = packet.Close()
		case <-packet.done:
		}
	}()
	if err := ctx.Err(); err != nil {
		_ = packet.Close()
		return nil, err
	}
	return packet, nil
}

func resolveTarget(ctx context.Context, target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", fmt.Errorf("invalid target %q: %w", target, err)
	}
	if net.ParseIP(host) != nil {
		return target, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("resolve target %q: %w", host, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("resolve target %q: no addresses", host)
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			return net.JoinHostPort(ip4.String(), port), nil
		}
	}
	return net.JoinHostPort(ips[0].String(), port), nil
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
	if err := socks5Handshake(conn, user); err != nil {
		return err
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
	if _, err := readSocksAddress(conn, header[3]); err != nil {
		return fmt.Errorf("read upstream SOCKS5 bind address: %w", err)
	}
	return nil
}

func socks5Handshake(conn net.Conn, user *url.Userinfo) error {
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

func readSocksAddress(reader io.Reader, addressType byte) (string, error) {
	var length int
	var host string
	switch addressType {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		nameLength := []byte{0}
		if _, err := io.ReadFull(reader, nameLength); err != nil {
			return "", err
		}
		if nameLength[0] == 0 {
			return "", errors.New("empty SOCKS5 domain address")
		}
		length = int(nameLength[0])
	default:
		return "", fmt.Errorf("unsupported address type %d", addressType)
	}
	address := make([]byte, length+2)
	if _, err := io.ReadFull(reader, address); err != nil {
		return "", err
	}
	if addressType == 3 {
		host = string(address[:length])
	} else {
		host = net.IP(address[:length]).String()
	}
	port := binary.BigEndian.Uint16(address[length:])
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

type socks5PacketConn struct {
	mu             sync.Mutex
	control        net.Conn
	udp            *net.UDPConn
	relay          *net.UDPAddr
	resolveDomains bool
	done           chan struct{}
	closed         bool
}

func (p *socks5PacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	packet := make([]byte, maxSOCKS5UDPPayload)
	count, _, err := p.udp.ReadFromUDP(packet)
	if err != nil {
		return 0, nil, err
	}
	if count < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return 0, nil, errors.New("invalid upstream SOCKS5 UDP packet")
	}
	reader := bytes.NewReader(packet[4:count])
	address, err := readSocksAddress(reader, packet[3])
	if err != nil {
		return 0, nil, err
	}
	consumed := count - 4 - reader.Len()
	payload := packet[4+consumed : count]
	if len(payload) > len(buffer) {
		copy(buffer, payload[:len(buffer)])
		return len(buffer), packetAddr(address), io.ErrShortBuffer
	}
	copy(buffer, payload)
	return len(payload), packetAddr(address), nil
}

func (p *socks5PacketConn) WriteTo(data []byte, address net.Addr) (int, error) {
	if address == nil {
		return 0, errors.New("datagram destination is required")
	}
	return p.WriteToTarget(data, address.String())
}

func (p *socks5PacketConn) WriteToTarget(data []byte, target string) (int, error) {
	if p.resolveDomains {
		resolveCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resolved, err := resolveTarget(resolveCtx, target)
		cancel()
		if err != nil {
			return 0, err
		}
		target = resolved
	}
	targetAddress, err := encodeTarget(target)
	if err != nil {
		return 0, err
	}
	packet := make([]byte, 0, 3+len(targetAddress)+len(data))
	packet = append(packet, 0, 0, 0)
	packet = append(packet, targetAddress...)
	packet = append(packet, data...)
	if _, err := p.udp.WriteToUDP(packet, p.relay); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (p *socks5PacketConn) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	control, udp := p.control, p.udp
	close(p.done)
	p.mu.Unlock()
	_ = control.Close()
	return udp.Close()
}
func (p *socks5PacketConn) LocalAddr() net.Addr                { return p.udp.LocalAddr() }
func (p *socks5PacketConn) SetDeadline(t time.Time) error      { return p.udp.SetDeadline(t) }
func (p *socks5PacketConn) SetReadDeadline(t time.Time) error  { return p.udp.SetReadDeadline(t) }
func (p *socks5PacketConn) SetWriteDeadline(t time.Time) error { return p.udp.SetWriteDeadline(t) }

type packetAddr string

func (a packetAddr) Network() string { return "udp" }
func (a packetAddr) String() string  { return string(a) }

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
