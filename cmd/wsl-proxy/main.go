package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/autoforward"
	appconfig "github.com/Kevin589981/wsl-win-relay/internal/config"
	"github.com/Kevin589981/wsl-win-relay/internal/forward"
	"github.com/Kevin589981/wsl-win-relay/internal/httpproxy"
	"github.com/Kevin589981/wsl-win-relay/internal/listencontrol"
	"github.com/Kevin589981/wsl-win-relay/internal/protocol"
	"github.com/Kevin589981/wsl-win-relay/internal/relay"
	"github.com/Kevin589981/wsl-win-relay/internal/socks5"
	"github.com/Kevin589981/wsl-win-relay/internal/transport/stdio"
)

type options struct {
	socksListen         string
	httpListen          string
	relayExe            string
	upstreamProxy       string
	reverse             forward.Mappings
	reverseUDP          forward.Mappings
	autoForward         bool
	autoForwardHost     string
	autoForwardHost6    string
	autoForwardInterval time.Duration
	autoInclude         map[uint16]bool
	autoExclude         map[uint16]bool
	controlSocket       string
	strictListenHost    string
	strictListenHost6   string
	udpAssociateIdle    time.Duration
}

var errRelayExited = errors.New("Windows relay exited")

const relayRestartDelay = 2 * time.Second
const relayRestartMaxDelay = 30 * time.Second
const relayRestartResetAfter = time.Minute

func main() {
	logger := log.New(os.Stderr, "wsl-proxy: ", log.LstdFlags)
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		logger.Printf("configuration: %v", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := supervise(ctx, opts, logger, run, relayRestartDelay); err != nil && !errors.Is(err, context.Canceled) {
		logger.Printf("stopped: %v", err)
		os.Exit(1)
	}
}

func supervise(ctx context.Context, opts options, logger *log.Logger, execute func(context.Context, options, *log.Logger) error, restartDelay time.Duration) error {
	currentDelay := restartDelay
	sessionStarted := time.Now()
	for {
		err := execute(ctx, opts, logger)
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, errRelayExited) {
			return err
		}
		currentDelay = resetRestartDelay(currentDelay, restartDelay, time.Since(sessionStarted), relayRestartResetAfter)
		logger.Printf("%v; restarting in %s", err, currentDelay)
		timer := time.NewTimer(currentDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		}
		currentDelay = nextRestartDelay(currentDelay, relayRestartMaxDelay)
		sessionStarted = time.Now()
	}
}

func resetRestartDelay(current, initial, elapsed, threshold time.Duration) time.Duration {
	if threshold > 0 && elapsed >= threshold {
		return initial
	}
	return current
}

func nextRestartDelay(current, maximum time.Duration) time.Duration {
	if current <= 0 || maximum <= 0 {
		return current
	}
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}

func parseOptions(args []string) (options, error) {
	configPath, err := findConfigPath(args)
	if err != nil {
		return options{}, err
	}
	fileConfig := appconfig.Default()
	if configPath != "" {
		fileConfig, err = appconfig.Load(configPath)
		if err != nil {
			return options{}, fmt.Errorf("load %s: %w", configPath, err)
		}
	}
	interval, err := fileConfig.AutoForwardDuration()
	if err != nil {
		return options{}, err
	}
	udpAssociateIdle, err := fileConfig.UDPAssociateIdleDuration()
	if err != nil {
		return options{}, err
	}
	opts := options{
		socksListen: fileConfig.SOCKS5Listen, httpListen: fileConfig.HTTPConnectListen,
		relayExe: fileConfig.RelayExecutable, upstreamProxy: fileConfig.UpstreamProxy, autoForward: fileConfig.AutoForward.Enabled,
		autoForwardHost: fileConfig.AutoForward.WindowsHost, autoForwardHost6: fileConfig.AutoForward.WindowsHost6, autoForwardInterval: interval,
		controlSocket: fileConfig.ControlSocket, strictListenHost: fileConfig.StrictListenHost, strictListenHost6: fileConfig.StrictListenHost6,
		udpAssociateIdle: udpAssociateIdle,
	}
	for _, mapping := range fileConfig.Reverse {
		if err := opts.reverse.Set(mapping); err != nil {
			return options{}, fmt.Errorf("config reverse: %w", err)
		}
	}
	for _, mapping := range fileConfig.ReverseUDP {
		if err := opts.reverseUDP.Set(mapping); err != nil {
			return options{}, fmt.Errorf("config reverse_udp: %w", err)
		}
	}
	include, exclude := formatPorts(fileConfig.AutoForward.Include), formatPorts(fileConfig.AutoForward.Exclude)
	set := flag.NewFlagSet("wsl-proxy", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&configPath, "config", configPath, "JSON configuration file")
	set.StringVar(&opts.socksListen, "listen", opts.socksListen, "SOCKS5 listen address")
	set.StringVar(&opts.httpListen, "http-listen", opts.httpListen, "optional HTTP CONNECT proxy listen address")
	set.StringVar(&opts.relayExe, "relay-exe", opts.relayExe, "Windows relay executable")
	set.StringVar(&opts.upstreamProxy, "upstream-proxy", opts.upstreamProxy, "optional Windows-side HTTP CONNECT or SOCKS5 proxy URL")
	set.Var(&opts.reverse, "reverse", "reverse mapping WINDOWS_ADDR=WSL_TARGET (repeatable)")
	set.Var(&opts.reverseUDP, "reverse-udp", "reverse UDP mapping WINDOWS_ADDR=WSL_TARGET (repeatable)")
	set.BoolVar(&opts.autoForward, "auto-forward", opts.autoForward, "automatically mirror WSL TCP listeners to Windows")
	set.StringVar(&opts.autoForwardHost, "auto-forward-host", opts.autoForwardHost, "Windows bind host for automatic mappings")
	set.StringVar(&opts.autoForwardHost6, "auto-forward-host6", opts.autoForwardHost6, "Windows IPv6 bind host for automatic mappings")
	set.DurationVar(&opts.autoForwardInterval, "auto-forward-interval", opts.autoForwardInterval, "automatic listener scan interval")
	set.StringVar(&include, "auto-forward-include", include, "comma-separated allowlist of ports for automatic mapping")
	set.StringVar(&exclude, "auto-forward-exclude", exclude, "comma-separated ports excluded from automatic mapping")
	set.StringVar(&opts.controlSocket, "control-socket", opts.controlSocket, "Unix socket for strict listener coordination; empty disables")
	set.StringVar(&opts.strictListenHost, "strict-listen-host", opts.strictListenHost, "Windows bind host for strict listener coordination")
	set.StringVar(&opts.strictListenHost6, "strict-listen-host6", opts.strictListenHost6, "Windows IPv6 bind host for strict listener coordination")
	set.DurationVar(&opts.udpAssociateIdle, "udp-associate-idle-timeout", opts.udpAssociateIdle, "idle timeout for SOCKS5 UDP associations")
	if err := set.Parse(args); err != nil {
		return options{}, err
	}
	if set.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(set.Args(), " "))
	}
	opts.autoInclude, err = parsePortSet(include)
	if err != nil {
		return options{}, fmt.Errorf("auto-forward-include: %w", err)
	}
	opts.autoExclude, err = parsePortSet(exclude)
	if err != nil {
		return options{}, fmt.Errorf("auto-forward-exclude: %w", err)
	}
	if opts.socksListen == "" {
		return options{}, errors.New("SOCKS5 listen address cannot be empty")
	}
	if opts.relayExe == "" {
		return options{}, errors.New("relay executable cannot be empty")
	}
	return opts, nil
}

func findConfigPath(args []string) (string, error) {
	var path string
	for index := 0; index < len(args); index++ {
		if args[index] == "-config" || args[index] == "--config" {
			if index+1 >= len(args) {
				return "", errors.New("-config requires a path")
			}
			path = args[index+1]
			index++
			continue
		}
		if strings.HasPrefix(args[index], "-config=") {
			path = strings.TrimPrefix(args[index], "-config=")
		}
		if strings.HasPrefix(args[index], "--config=") {
			path = strings.TrimPrefix(args[index], "--config=")
		}
	}
	return path, nil
}

func formatPorts(ports []uint16) string {
	copyPorts := append([]uint16(nil), ports...)
	sort.Slice(copyPorts, func(i, j int) bool { return copyPorts[i] < copyPorts[j] })
	values := make([]string, len(copyPorts))
	for index, port := range copyPorts {
		values[index] = strconv.Itoa(int(port))
	}
	return strings.Join(values, ",")
}

func run(parent context.Context, opts options, logger *log.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	socksListener, err := net.Listen("tcp", opts.socksListen)
	if err != nil {
		return fmt.Errorf("SOCKS5 listen on %s: %w", opts.socksListen, err)
	}
	defer socksListener.Close()
	var httpListener net.Listener
	if opts.httpListen != "" {
		httpListener, err = net.Listen("tcp", opts.httpListen)
		if err != nil {
			return fmt.Errorf("HTTP proxy listen on %s: %w", opts.httpListen, err)
		}
		defer httpListener.Close()
	}

	cmd := exec.CommandContext(ctx, opts.relayExe, relayArguments(opts)...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open relay stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open relay stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start relay %q: %w", opts.relayExe, err)
	}
	endpoint := stdio.New(stdout, stdin, func() error { _ = stdin.Close(); _ = stdout.Close(); return nil })
	client := relay.NewClient(endpoint)
	var reverseForwards *forward.Set
	var reverseDatagramForwards *forward.Set
	defer func() {
		if reverseForwards != nil {
			_ = reverseForwards.Close()
		}
		if reverseDatagramForwards != nil {
			_ = reverseDatagramForwards.Close()
		}
		_ = client.Close()
		_ = endpoint.Close()
		cancel()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitDone := make(chan struct{})
		go func() { _ = cmd.Wait(); close(waitDone) }()
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
			logger.Printf("relay process did not exit promptly")
		}
	}()

	relayDone := make(chan error, 1)
	go func() { relayDone <- client.Run(ctx) }()
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 5*time.Second)
	capabilities, err := client.Handshake(handshakeCtx, protocol.AllCapabilities)
	handshakeCancel()
	if err != nil {
		return classifyHandshakeError(err)
	}
	logger.Printf("Windows relay ready (capabilities 0x%x)", capabilities)
	var controlDone chan error
	if opts.controlSocket != "" {
		control := &listencontrol.Server{Path: opts.controlSocket, WindowsHost: opts.strictListenHost, WindowsHost6: opts.strictListenHost6, Reserve: func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
			return client.ReserveReverseForward(reserveCtx, windows, wsl)
		}, ReserveDatagram: func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
			closer, err := client.ReverseDatagramForward(reserveCtx, windows, wsl)
			if err != nil {
				return nil, err
			}
			return noCommitReservation{Closer: closer}, nil
		}}
		controlDone = make(chan error, 1)
		go func() { controlDone <- control.Serve(ctx) }()
		logger.Printf("strict-listen control socket %s", opts.controlSocket)
	}
	reverseForwards, err = forward.OpenAll(ctx, client, opts.reverse)
	if err != nil {
		return fmt.Errorf("register reverse forwards: %w", err)
	}
	reverseDatagramForwards, err = forward.OpenDatagramAll(ctx, client, opts.reverseUDP)
	if err != nil {
		return fmt.Errorf("register reverse UDP forwards: %w", err)
	}
	for _, mapping := range opts.reverse {
		logger.Printf("reverse forwarding %s -> %s", mapping.Windows, mapping.WSL)
	}
	for _, mapping := range opts.reverseUDP {
		logger.Printf("reverse UDP forwarding %s -> %s", mapping.Windows, mapping.WSL)
	}

	logger.Printf("SOCKS5 listening on %s", socksListener.Addr())
	proxy := &socks5.Server{Listener: socksListener, Dialer: client, Logger: logger, UDPAssociateIdleTimeout: opts.udpAssociateIdle}
	socksDone := make(chan error, 1)
	go func() { socksDone <- proxy.Serve(ctx) }()
	var httpDone chan error
	if httpListener != nil {
		httpProxy := &httpproxy.Server{Listener: httpListener, Dialer: client, Logger: logger}
		httpDone = make(chan error, 1)
		go func() { httpDone <- httpProxy.Serve(ctx) }()
		logger.Printf("HTTP CONNECT proxy listening on %s", httpListener.Addr())
	}

	var autoDone chan error
	if opts.autoForward {
		excluded := clonePortSet(opts.autoExclude)
		addAddressPort(excluded, socksListener.Addr().String())
		if httpListener != nil {
			addAddressPort(excluded, httpListener.Addr().String())
		}
		for _, mapping := range opts.reverse {
			addAddressPort(excluded, mapping.Windows)
			addAddressPort(excluded, mapping.WSL)
		}
		for _, mapping := range opts.reverseUDP {
			addAddressPort(excluded, mapping.Windows)
			addAddressPort(excluded, mapping.WSL)
		}
		watcher := &autoforward.Watcher{Scanner: autoforward.DefaultProcScanner(), Opener: client, WindowsHost: opts.autoForwardHost, WindowsHost6: opts.autoForwardHost6, Interval: opts.autoForwardInterval, Included: opts.autoInclude, Excluded: excluded, Logger: logger}
		autoDone = make(chan error, 1)
		go func() { autoDone <- watcher.Run(ctx) }()
		logger.Printf("automatic forwarding enabled on Windows hosts %s (IPv4), %s (IPv6)", opts.autoForwardHost, opts.autoForwardHost6)
	}

	select {
	case err := <-relayDone:
		err = sessionCompletion(ctx, err)
		if ctx.Err() != nil {
			return err
		}
		if isRelayTransportExit(err) {
			return errRelayExited
		}
		return err
	case err := <-socksDone:
		return sessionCompletion(ctx, err)
	case err := <-httpDone:
		return sessionCompletion(ctx, err)
	case err := <-autoDone:
		return sessionCompletion(ctx, err)
	case err := <-controlDone:
		return sessionCompletion(ctx, err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isRelayTransportExit(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe)
}

func sessionCompletion(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func relayArguments(opts options) []string {
	// exec.Command supplies the executable path as argv[0]. The Windows relay
	// has no positional subcommand, so only pass actual flags here.
	if opts.upstreamProxy == "" {
		return nil
	}
	return []string{"-upstream-proxy", opts.upstreamProxy}
}

type noCommitReservation struct{ io.Closer }

func (noCommitReservation) Commit() error { return nil }

func classifyHandshakeError(err error) error {
	if isRelayTransportExit(err) || errors.Is(err, relay.ErrClientClosed) {
		return errRelayExited
	}
	return fmt.Errorf("relay handshake: %w", err)
}

func clonePortSet(source map[uint16]bool) map[uint16]bool {
	result := make(map[uint16]bool, len(source))
	for port, enabled := range source {
		result[port] = enabled
	}
	return result
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
