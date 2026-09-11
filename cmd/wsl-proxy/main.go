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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/autoforward"
	"github.com/Kevin589981/wsl-win-relay/internal/forward"
	"github.com/Kevin589981/wsl-win-relay/internal/listencontrol"
	"github.com/Kevin589981/wsl-win-relay/internal/relay"
	"github.com/Kevin589981/wsl-win-relay/internal/socks5"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:1080", "SOCKS5 listen address")
	relayExe := flag.String("relay-exe", "wsl-win-relay.exe", "Windows relay executable")
	var reverseMappings forward.Mappings
	flag.Var(&reverseMappings, "reverse", "reverse mapping WINDOWS_ADDR=WSL_TARGET (repeatable)")
	autoForward := flag.Bool("auto-forward", false, "automatically mirror WSL TCP listeners to Windows")
	autoForwardHost := flag.String("auto-forward-host", "127.0.0.1", "Windows bind host for automatic mappings")
	autoForwardInterval := flag.Duration("auto-forward-interval", time.Second, "automatic listener scan interval")
	autoForwardInclude := flag.String("auto-forward-include", "", "comma-separated allowlist of ports for automatic mapping")
	autoForwardExclude := flag.String("auto-forward-exclude", "", "comma-separated ports excluded from automatic mapping")
	controlSocket := flag.String("control-socket", "/tmp/wsl-win-relay-control.sock", "Unix socket for strict listener coordination; empty disables")
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
	var controlDone chan error
	if *controlSocket != "" {
		control := &listencontrol.Server{Path: *controlSocket, Reserve: func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
			return client.ReserveReverseForward(reserveCtx, windows, wsl)
		}}
		controlDone = make(chan error, 1)
		go func() { controlDone <- control.Serve(ctx) }()
		logger.Printf("strict-listen control socket %s", *controlSocket)
	}
	reverseForwards, err := forward.OpenAll(ctx, client, reverseMappings)
	if err != nil {
		logger.Fatalf("register reverse forwards: %v", err)
	}
	for _, mapping := range reverseMappings {
		logger.Printf("reverse forwarding %s -> %s", mapping.Windows, mapping.WSL)
	}

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		_ = endpoint.Close()
		_ = cmd.Process.Kill()
		logger.Fatalf("listen on %s: %v", *listenAddr, err)
	}
	logger.Printf("SOCKS5 listening on %s", listener.Addr())
	var autoDone chan error
	if *autoForward {
		included, parseErr := parsePortSet(*autoForwardInclude)
		if parseErr != nil {
			logger.Fatalf("parse -auto-forward-include: %v", parseErr)
		}
		excluded, parseErr := parsePortSet(*autoForwardExclude)
		if parseErr != nil {
			logger.Fatalf("parse -auto-forward-exclude: %v", parseErr)
		}
		addAddressPort(excluded, listener.Addr().String())
		for _, mapping := range reverseMappings {
			addAddressPort(excluded, mapping.WSL)
		}
		watcher := &autoforward.Watcher{Scanner: autoforward.DefaultProcScanner(), Opener: client, WindowsHost: *autoForwardHost, Interval: *autoForwardInterval, Included: included, Excluded: excluded, Logger: logger}
		autoDone = make(chan error, 1)
		go func() { autoDone <- watcher.Run(ctx) }()
		logger.Printf("automatic forwarding enabled on Windows host %s", *autoForwardHost)
	}
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
	case err := <-autoDone:
		if err != nil {
			logger.Printf("automatic forwarding stopped: %v", err)
		}
	case err := <-controlDone:
		if err != nil {
			logger.Printf("strict-listen control stopped: %v", err)
		}
	}

	stop()
	_ = listener.Close()
	_ = client.Close()
	_ = reverseForwards.Close()
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

func parsePortSet(value string) (map[uint16]bool, error) {
	result := make(map[uint16]bool)
	if strings.TrimSpace(value) == "" {
		return result, nil
	}
	for _, raw := range strings.Split(value, ",") {
		port, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 16)
		if err != nil || port == 0 {
			return nil, errors.New("ports must be integers between 1 and 65535")
		}
		result[uint16(port)] = true
	}
	return result, nil
}

func addAddressPort(set map[uint16]bool, address string) {
	_, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err == nil && port != 0 {
		set[uint16(port)] = true
	}
}
