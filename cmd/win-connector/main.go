package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Kevin589981/wsl-win-relay/internal/connector"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
)

type options struct {
	endpoint  string
	tokenHex  string
	lastEpoch uint64
}

func main() {
	logger := log.New(os.Stderr, "win-connector: ", log.LstdFlags)
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		logger.Printf("configuration: %v", err)
		os.Exit(2)
	}
	token, _ := hex.DecodeString(opts.tokenHex)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	session, err := connector.Connect(ctx, connector.Config{Endpoint: opts.endpoint, Token: token, LastEpoch: opts.lastEpoch})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.Printf("attach: %v", err)
		}
		os.Exit(1)
	}
	defer session.Close()
	endpoint := stdio.New(os.Stdin, os.Stdout, nil)
	if err := connector.Bridge(ctx, endpoint, session.Conn); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		logger.Printf("bridge: %v", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	opts := options{endpoint: defaultEndpoint(), tokenHex: os.Getenv("WSL_WIN_RELAY_ATTACH_TOKEN")}
	set := flag.NewFlagSet("win-connector", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&opts.endpoint, "endpoint", opts.endpoint, "per-user local IPC endpoint")
	set.StringVar(&opts.tokenHex, "token-hex", opts.tokenHex, "attach token in hexadecimal (prefer WSL_WIN_RELAY_ATTACH_TOKEN)")
	set.Uint64Var(&opts.lastEpoch, "last-epoch", 0, "last broker attach epoch observed by this connector")
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
		return options{}, err
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
