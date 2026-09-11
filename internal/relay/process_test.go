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
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(wd, "..", "..", "bin", "wsl-win-relay.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Skipf("build %s first: %v", exe, err)
	}

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		conn, acceptErr := echo.Accept()
		if acceptErr == nil {
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
		}
	}()

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
	conn, err := client.DialContext(ctx, echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("process-pipe")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("process-pipe"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "process-pipe" {
		t.Fatalf("got %q", buf)
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
