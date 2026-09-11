package relay

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
)

func TestWindowsRelayProcess(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("requires Windows or WSL interop")
	}
	if os.Getenv("WSL_WIN_RELAY_E2E") != "1" {
		t.Skip("set WSL_WIN_RELAY_E2E=1 after building the Windows relay")
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err != nil {
			t.Skip("requires WSL interop to launch the Windows relay")
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(wd, "..", "..", "bin", "wsl-win-relay.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Skipf("build %s first: %v", exe, err)
	}

	target := "example.com:80"
	var echo net.Listener
	if runtime.GOOS == "windows" {
		echo, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer echo.Close()
		target = echo.Addr().String()
		go func() {
			conn, acceptErr := echo.Accept()
			if acceptErr == nil {
				_, _ = io.Copy(conn, conn)
				_ = conn.Close()
			}
		}()
	}

	cmd := exec.Command(exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	endpoint := stdio.New(stdout, stdin, func() error { _ = stdin.Close(); _ = stdout.Close(); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewClient(endpoint)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	if capabilities, err := client.Handshake(ctx, protocol.AllCapabilities); err != nil || capabilities != protocol.AllCapabilities {
		t.Fatalf("handshake capabilities=0x%x err=%v", capabilities, err)
	}
	conn, err := client.DialContext(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	payload := []byte("process-pipe")
	if runtime.GOOS == "linux" {
		payload = []byte("HEAD / HTTP/1.0\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatal(err)
		}
		if string(buf) != string(payload) {
			t.Fatalf("got %q", buf)
		}
	} else {
		buf, err := io.ReadAll(conn)
		if err != nil {
			t.Fatal(err)
		}
		if len(buf) == 0 {
			t.Fatal("empty HTTP response")
		}
		testWindowsUDP(t, ctx, client)
	}
	_ = conn.Close()
	_ = client.Close()
	_ = endpoint.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
}

func testWindowsUDP(t *testing.T, ctx context.Context, client *Client) {
	t.Helper()
	packet, err := client.OpenPacketContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	_ = packet.SetDeadline(time.Now().Add(5 * time.Second))
	query := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x07, 'e', 'x', 'a',
		'm', 'p', 'l', 'e', 0x03, 'c', 'o', 'm', 0x00,
		0x00, 0x01, 0x00, 0x01,
	}
	if _, err := packet.WriteTo(query, relayAddr("1.1.1.1:53")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 1500)
	count, _, err := packet.ReadFrom(response)
	if err != nil {
		t.Fatal(err)
	}
	if count < 12 || response[0] != 0x12 || response[1] != 0x34 || response[2]&0x80 == 0 {
		t.Fatalf("invalid DNS response: %x", response[:count])
	}
}
