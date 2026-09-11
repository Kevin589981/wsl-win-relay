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

	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
)

func TestWindowsRelayProcess(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("requires Windows or WSL interop")
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err != nil {
			t.Skip("requires WSL interop to launch the Windows relay")
		}
		if os.Getenv("WSL_WIN_RELAY_E2E") != "1" {
			t.Skip("set WSL_WIN_RELAY_E2E=1 to run the network-dependent WSL check")
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

	cmd := exec.Command(exe, "win-relay")
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
