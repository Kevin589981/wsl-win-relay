package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"

	"github.com/Kevin589981/wsl-win-relay/internal/relay"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
)

func main() {
	logger := log.New(os.Stderr, "win-relay: ", log.LstdFlags)
	endpoint := stdio.New(os.Stdin, os.Stdout, nil)
	server := relay.NewServer(endpoint, nil)
	if err := server.Serve(context.Background()); err != nil && !errors.Is(err, io.EOF) {
		logger.Printf("stopped: %v", err)
		os.Exit(1)
	}
}
