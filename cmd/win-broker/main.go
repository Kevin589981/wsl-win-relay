package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type options struct {
	endpoint      string
	tokenHex      string
	tokenFile     string
	upstreamProxy string
	supervisor    bool
	worker        bool
	socketHost    bool
	socketBridge  bool
	socketOwner   bool
	ownerEndpoint string
}

func main() {
	logger := log.New(os.Stderr, "win-broker: ", log.LstdFlags)
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		logger.Printf("configuration: %v", err)
		os.Exit(2)
	}
	if opts.socketHost {
		if err := runSocketBridge(opts, logger, roleSocketHost); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("stopped: %v", err)
			os.Exit(1)
		}
		return
	}
	if opts.supervisor {
		if err := runSupervisor(opts, logger); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("stopped: %v", err)
			os.Exit(1)
		}
		return
	}
	if opts.socketBridge {
		if err := runSocketBridge(opts, logger, roleSocketBridge); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("stopped: %v", err)
			os.Exit(1)
		}
		return
	}
	if opts.socketOwner {
		if err := runSocketOwner(opts, logger); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("stopped: %v", err)
			os.Exit(1)
		}
		return
	}
	if opts.worker {
		if err := runWorker(opts, logger); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("stopped: %v", err)
			os.Exit(1)
		}
		return
	}
	if err := runFrontend(opts, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Printf("stopped: %v", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	opts := options{endpoint: defaultEndpoint(), tokenHex: os.Getenv("WSL_WIN_RELAY_ATTACH_TOKEN"), upstreamProxy: os.Getenv("WSL_WIN_RELAY_UPSTREAM_PROXY")}
	set := flag.NewFlagSet("win-broker", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&opts.endpoint, "endpoint", opts.endpoint, "per-user local IPC endpoint")
	set.StringVar(&opts.tokenHex, "token-hex", opts.tokenHex, "attach token in hexadecimal (prefer WSL_WIN_RELAY_ATTACH_TOKEN)")
	set.StringVar(&opts.tokenFile, "token-file", "", "private file containing the hexadecimal attach token")
	set.StringVar(&opts.upstreamProxy, "upstream-proxy", opts.upstreamProxy, "optional HTTP CONNECT or SOCKS5 proxy URL")
	set.BoolVar(&opts.supervisor, "supervise", false, "host-level supervisor mode; restart the frontend after a crash")
	set.BoolVar(&opts.worker, "worker", false, "internal socket-owning worker mode")
	set.BoolVar(&opts.socketHost, "socket-host", false, "internal durable socket-host mode")
	set.BoolVar(&opts.socketBridge, "socket-bridge", false, "internal connector bridge mode")
	set.BoolVar(&opts.socketOwner, "socket-owner", false, "internal durable socket-owner mode")
	set.StringVar(&opts.ownerEndpoint, "owner-endpoint", "", "internal socket-owner endpoint")
	if err := set.Parse(args); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, errors.New("unexpected arguments: " + strings.Join(set.Args(), " "))
	}
	if opts.tokenFile != "" {
		loaded, err := loadTokenFile(opts.tokenFile)
		if err != nil {
			return options{}, err
		}
		opts.tokenHex = loaded
	}
	if opts.endpoint == "" || opts.tokenHex == "" {
		return options{}, errors.New("endpoint and token-hex are required")
	}
	roleCount := 0
	if opts.supervisor {
		roleCount++
	}
	if opts.worker {
		roleCount++
	}
	if opts.socketHost {
		roleCount++
		if opts.ownerEndpoint == "" {
			return options{}, errors.New("owner-endpoint is required for socket-host mode")
		}
	}
	if opts.socketBridge {
		roleCount++
		if opts.ownerEndpoint == "" {
			return options{}, errors.New("owner-endpoint is required for socket-bridge mode")
		}
	}
	if opts.socketOwner {
		roleCount++
	}
	if roleCount > 1 {
		return options{}, errors.New("worker, socket-host, socket-bridge, and socket-owner modes are mutually exclusive")
	}
	if _, err := hex.DecodeString(opts.tokenHex); err != nil {
		return options{}, fmt.Errorf("token-hex: %w", err)
	}
	return opts, nil
}

func defaultEndpoint() string {
	if endpoint := os.Getenv("WSL_WIN_RELAY_BROKER_ENDPOINT"); endpoint != "" {
		return endpoint
	}
	if runtime.GOOS == "windows" {
		return "wsl-win-relay-broker"
	}
	return filepath.Join(os.TempDir(), "wsl-win-relay-broker.sock")
}
