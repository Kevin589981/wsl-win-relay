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
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Kevin589981/wsl-win-relay/internal/broker"
	"github.com/Kevin589981/wsl-win-relay/internal/relay"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/attach"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/framed"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/localipc"
	"github.com/Kevin589981/wsl-win-relay/internal/upstream"
)

type options struct {
	endpoint      string
	tokenHex      string
	upstreamProxy string
}

func main() {
	logger := log.New(os.Stderr, "win-broker: ", log.LstdFlags)
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		logger.Printf("configuration: %v", err)
		os.Exit(2)
	}
	token, err := hex.DecodeString(opts.tokenHex)
	if err != nil || len(token) == 0 {
		logger.Printf("attach token must be non-empty hexadecimal")
		os.Exit(2)
	}
	registry, err := attach.NewWithToken(token)
	if err != nil {
		logger.Printf("attach registry: %v", err)
		os.Exit(2)
	}
	upstreamDialer, err := upstream.New(opts.upstreamProxy)
	if err != nil {
		logger.Printf("upstream proxy: %v", err)
		os.Exit(2)
	}
	listener, err := localipc.Listen(opts.endpoint)
	if err != nil {
		logger.Printf("listen %s: %v", opts.endpoint, err)
		os.Exit(1)
	}
	defer listener.Close()
	service, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	b := broker.NewWithRegistry(registry, 0)
	link := framed.New()
	server := relay.NewServerWithLink(link, upstreamDialer.DialContext, upstreamDialer.OpenPacketContext)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeAttached(service) }()
	logger.Printf("broker listening on %s", opts.endpoint)
	err = b.ServeAttached(service, listener, link)
	_ = link.Close()
	serverErr := <-serverDone
	if err == nil && serverErr != nil && !errors.Is(serverErr, context.Canceled) && !errors.Is(serverErr, framed.ErrClosed) {
		err = serverErr
	}
	if err != nil && !errors.Is(err, context.Canceled) {
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
	set.StringVar(&opts.upstreamProxy, "upstream-proxy", opts.upstreamProxy, "optional HTTP CONNECT or SOCKS5 proxy URL")
	if err := set.Parse(args); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, errors.New("unexpected arguments: " + strings.Join(set.Args(), " "))
	}
	if opts.endpoint == "" || opts.tokenHex == "" {
		return options{}, errors.New("endpoint and token-hex are required")
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
