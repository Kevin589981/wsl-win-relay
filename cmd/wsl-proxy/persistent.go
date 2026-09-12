package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/forward"
	"github.com/Kevin589981/wsl-win-relay/internal/listencontrol"
	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
	"github.com/Kevin589981/wsl-win-relay/internal/relay"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/framed"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
)

// runPersistent owns one relay client and Link for the whole proxy process.
// Connector children are disposable attachments; the stream registry and
// broker-side sockets remain owned by these long-lived objects.
func runPersistent(parent context.Context, opts options, logger *log.Logger, dialer *sessionDialer, control *listencontrol.Server) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	link := framed.New()
	client := relay.NewClientWithLink(link)
	relayDone := make(chan error, 1)
	go func() { relayDone <- client.RunAttached(ctx) }()
	var reverseForwards *forward.Set
	var reverseDatagramForwards *forward.Set
	initialized := false
	var peerInstanceID uint64
	defer func() {
		if reverseForwards != nil {
			_ = reverseForwards.Close()
		}
		if reverseDatagramForwards != nil {
			_ = reverseDatagramForwards.Close()
		}
		dialer.clear(client)
		_ = client.Close()
		_ = link.Close()
	}()
	return supervise(ctx, opts, logger, func(sessionCtx context.Context, sessionOpts options, sessionLogger *log.Logger) error {
		return runPersistentConnector(sessionCtx, ctx, sessionOpts, sessionLogger, dialer, link, client, relayDone, &initialized, &peerInstanceID, &reverseForwards, &reverseDatagramForwards, control)
	}, relayRestartDelay)
}

func runPersistentConnector(parent, mappingCtx context.Context, opts options, logger *log.Logger, dialer *sessionDialer, link *framed.Link, client *relay.Client, relayDone <-chan error, initialized *bool, peerInstanceID *uint64, reverseForwards **forward.Set, reverseDatagramForwards **forward.Set, control *listencontrol.Server) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := exec.CommandContext(ctx, opts.relayExe, relayArguments(opts)...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open connector stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open connector stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start connector %q: %w", opts.relayExe, err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()
	processWaited := false
	var processErr error
	waitProcess := func(timeout time.Duration) bool {
		if processWaited {
			return true
		}
		if timeout <= 0 {
			select {
			case processErr = <-processDone:
				processWaited = true
				return true
			default:
				return false
			}
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case processErr = <-processDone:
			processWaited = true
			return true
		case <-timer.C:
			return false
		}
	}
	endpoint := stdio.New(stdout, stdin, func() error { _ = stdin.Close(); _ = stdout.Close(); return nil })
	defer func() {
		_ = endpoint.Close()
		cancel()
		if cmd.Process != nil && !processWaited {
			_ = cmd.Process.Kill()
		}
		if !waitProcess(2 * time.Second) {
			logger.Printf("connector process did not exit promptly")
		}
	}()
	if _, err := link.Attach(endpoint); err != nil {
		return err
	}
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, opts.relayHandshakeTimeout)
	var capabilities uint64
	if *initialized {
		capabilities, err = client.Rehandshake(handshakeCtx, protocol.AllCapabilities)
	} else {
		capabilities, err = client.Handshake(handshakeCtx, protocol.AllCapabilities)
	}
	handshakeCancel()
	if err != nil {
		if waitProcess(100 * time.Millisecond) {
			if fatal := classifyRelayProcessExit(processErr); fatal != nil {
				return fatal
			}
		}
		return classifyHandshakeError(err)
	}
	instanceID := client.PeerInstanceID()
	if *initialized && instanceID != 0 && *peerInstanceID != 0 && instanceID != *peerInstanceID {
		logger.Printf("Windows broker instance changed; rebuilding peer-owned mappings")
		client.ResetPeerState(relay.ErrPeerRestarted)
		if *reverseForwards != nil {
			_ = (*reverseForwards).Close()
			*reverseForwards = nil
		}
		if *reverseDatagramForwards != nil {
			_ = (*reverseDatagramForwards).Close()
			*reverseDatagramForwards = nil
		}
		dialer.clear(client)
		*initialized = false
	}
	if instanceID != 0 {
		*peerInstanceID = instanceID
	}
	if !*initialized {
		dialer.set(client)
		*reverseForwards, *reverseDatagramForwards, err = registerMappings(mappingCtx, opts, logger, client, control)
		if err != nil {
			return err
		}
		*initialized = true
	}
	logger.Printf("Windows broker connector ready (capabilities 0x%x)", capabilities)
	select {
	case err := <-processDone:
		processWaited = true
		processErr = err
		if fatal := classifyRelayProcessExit(err); fatal != nil {
			return fatal
		}
		return errRelayExited
	case err := <-relayDone:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func registerMappings(ctx context.Context, opts options, logger *log.Logger, client *relay.Client, control *listencontrol.Server) (*forward.Set, *forward.Set, error) {
	reverseForwards, err := forward.OpenAll(ctx, client, opts.reverse)
	if err != nil {
		return nil, nil, fmt.Errorf("register reverse forwards: %w", err)
	}
	reverseDatagramForwards, err := forward.OpenDatagramAll(ctx, client, opts.reverseUDP)
	if err != nil {
		_ = reverseForwards.Close()
		return nil, nil, fmt.Errorf("register reverse UDP forwards: %w", err)
	}
	for _, mapping := range opts.reverse {
		logger.Printf("reverse forwarding %s -> %s", mapping.Windows, mapping.WSL)
	}
	for _, mapping := range opts.reverseUDP {
		logger.Printf("reverse UDP forwarding %s -> %s", mapping.Windows, mapping.WSL)
	}
	if control != nil {
		rebindCtx, rebindCancel := context.WithTimeout(ctx, opts.relayDialTimeout)
		rebindErr := control.Rebind(rebindCtx,
			func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
				return client.ReserveReverseForward(reserveCtx, windows, wsl)
			},
			func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
				closer, err := client.ReverseDatagramForward(reserveCtx, windows, wsl)
				if err != nil {
					return nil, err
				}
				return noCommitReservation{Closer: closer}, nil
			},
		)
		rebindCancel()
		if rebindErr != nil {
			logger.Printf("strict-listen lease rebind: %v", rebindErr)
		}
	}
	return reverseForwards, reverseDatagramForwards, nil
}
