package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"strings"

	"github.com/Kevin589981/wsl-win-relay/internal/buildinfo"
	"github.com/Kevin589981/wsl-win-relay/internal/relay"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
	"github.com/Kevin589981/wsl-win-relay/internal/upstream"
)

type options struct {
	upstreamProxy string
}

func main() {
	if buildinfo.PrintRequested(os.Stdout, "wsl-win-relay", os.Args[1:]) {
		return
	}
	logger := log.New(os.Stderr, "win-relay: ", log.LstdFlags)
	logger.Print(buildinfo.String("wsl-win-relay"))
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		logger.Printf("configuration: %v", err)
		os.Exit(2)
	}
	endpoint := stdio.New(os.Stdin, os.Stdout, nil)
	dialer, err := upstream.New(opts.upstreamProxy)
	if err != nil {
		logger.Printf("upstream proxy: %v", err)
		os.Exit(2)
	}
	server := relay.NewServerWithPacketDialer(endpoint, dialer.DialContext, dialer.OpenPacketContext)
	if err := server.Serve(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		logger.Printf("stopped: %v", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	opts := options{upstreamProxy: os.Getenv("WSL_WIN_RELAY_UPSTREAM_PROXY")}
	set := flag.NewFlagSet("win-relay", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&opts.upstreamProxy, "upstream-proxy", opts.upstreamProxy, "optional HTTP CONNECT or SOCKS5 proxy URL")
	if err := set.Parse(args); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, errors.New("unexpected arguments: " + strings.Join(set.Args(), " "))
	}
	return opts, nil
}
