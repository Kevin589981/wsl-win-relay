package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/relay"
	"github.com/Kevin589981/wsl-win-relay/internal/socks5"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:1080", "SOCKS5 listen address")
	relayExe := flag.String("relay-exe", "wsl-win-relay.exe", "Windows relay executable")
	reverse := flag.String("reverse", "", "reverse mapping WINDOWS_ADDR=WSL_TARGET")
	flag.Parse()

	logger := log.New(os.Stderr, "wsl-proxy: ", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := exec.CommandContext(ctx, *relayExe, "win-relay")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		logger.Fatalf("open relay stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logger.Fatalf("open relay stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		logger.Fatalf("start relay %q: %v", *relayExe, err)
	}

	endpoint := stdio.New(stdout, stdin, func() error {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil
	})
	client := relay.NewClient(endpoint)
	relayDone := make(chan error, 1)
	go func() { relayDone <- client.Run(ctx) }()
	var reverseForward io.Closer
	if *reverse != "" {
		parts := strings.SplitN(*reverse, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			logger.Fatalf("invalid -reverse %q; expected WINDOWS_ADDR=WSL_TARGET", *reverse)
		}
		reverseForward, err = client.ReverseForward(ctx, parts[0], parts[1])
		if err != nil {
			logger.Fatalf("reverse forward %q: %v", *reverse, err)
		}
		logger.Printf("reverse forwarding %s -> %s", parts[0], parts[1])
	}

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		_ = endpoint.Close()
		_ = cmd.Process.Kill()
		logger.Fatalf("listen on %s: %v", *listenAddr, err)
	}
	logger.Printf("SOCKS5 listening on %s", listener.Addr())
	proxy := &socks5.Server{Listener: listener, Dialer: client, Logger: logger}
	serveDone := make(chan error, 1)
	go func() { serveDone <- proxy.Serve(ctx) }()

	select {
	case err := <-relayDone:
		if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			logger.Printf("relay stopped: %v", err)
		}
	case err := <-serveDone:
		if err != nil {
			logger.Printf("proxy stopped: %v", err)
		}
	case <-ctx.Done():
	}

	stop()
	_ = listener.Close()
	_ = client.Close()
	if reverseForward != nil {
		_ = reverseForward.Close()
	}
	_ = endpoint.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		logger.Printf("relay process did not exit promptly")
	}
}
