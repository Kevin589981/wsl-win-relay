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
	"github.com/Kevin589981/wsl-win-relay/internal/buildinfo"
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
	checkConfig           bool
	socksListen           string
	httpListen            string
	relayExe              string
	brokerMode            bool
	upstreamProxy         string
	reverse               forward.Mappings
	reverseUDP            forward.Mappings
	autoForward           bool
	autoForwardUDP        bool
	autoForwardHost       string
	autoForwardHost6      string
	autoForwardPortOffset int
	autoForwardPortAuto   bool
	autoForwardStatus     string
	autoForwardInterval   time.Duration
	autoRetryMin          time.Duration
	autoRetryMax          time.Duration
	autoInclude           map[uint16]bool
	autoUDPInclude        map[uint16]bool
	autoExclude           map[uint16]bool
	controlSocket         string
	strictListenHost      string
	strictListenHost6     string
	udpAssociateIdle      time.Duration
	relayHandshakeTimeout time.Duration
	relayDialTimeout      time.Duration
	proxyHandshakeTimeout time.Duration
}

var errRelayExited = errors.New("Windows relay exited")

const relayRestartDelay = 2 * time.Second
const relayRestartMaxDelay = 30 * time.Second
const relayRestartResetAfter = time.Minute

func main() {
	if buildinfo.PrintRequested(os.Stdout, "wsl-proxy", os.Args[1:]) {
		return
	}
	logger := log.New(os.Stderr, "wsl-proxy: ", log.LstdFlags)
	logger.Print(buildinfo.String("wsl-proxy"))
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		logger.Printf("configuration: %v", err)
		os.Exit(2)
	}
	if opts.checkConfig {
		fmt.Fprintln(os.Stdout, "configuration valid")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts, logger); err != nil && !errors.Is(err, context.Canceled) {
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
	autoRetryMin, autoRetryMax, err := fileConfig.AutoForwardRetryDurations()
	if err != nil {
		return options{}, err
	}
	udpAssociateIdle, err := fileConfig.UDPAssociateIdleDuration()
	if err != nil {
		return options{}, err
	}
	relayDialTimeout, err := fileConfig.RelayDialDuration()
	if err != nil {
		return options{}, err
	}
	relayHandshakeTimeout, err := fileConfig.RelayHandshakeDuration()
	if err != nil {
		return options{}, err
	}
	proxyHandshakeTimeout, err := fileConfig.ProxyHandshakeDuration()
	if err != nil {
		return options{}, err
	}
	opts := options{
		socksListen: fileConfig.SOCKS5Listen, httpListen: fileConfig.HTTPProxyListen, brokerMode: fileConfig.BrokerMode,
		relayExe: fileConfig.RelayExecutable, upstreamProxy: fileConfig.UpstreamProxy, autoForward: fileConfig.AutoForward.Enabled,
		autoForwardHost: fileConfig.AutoForward.WindowsHost, autoForwardHost6: fileConfig.AutoForward.WindowsHost6, autoForwardInterval: interval,
		autoForwardPortOffset: fileConfig.AutoForward.WindowsPortOffset,
		autoForwardPortAuto:   fileConfig.AutoForward.WindowsPortAuto,
		autoForwardStatus:     fileConfig.AutoForward.StatusFile,
		autoRetryMin:          autoRetryMin, autoRetryMax: autoRetryMax,
		controlSocket: fileConfig.ControlSocket, strictListenHost: fileConfig.StrictListenHost, strictListenHost6: fileConfig.StrictListenHost6,
		udpAssociateIdle:      udpAssociateIdle,
		relayHandshakeTimeout: relayHandshakeTimeout,
		relayDialTimeout:      relayDialTimeout,
		proxyHandshakeTimeout: proxyHandshakeTimeout,
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
	udpInclude := formatPorts(fileConfig.AutoForward.UDPInclude)
	set := flag.NewFlagSet("wsl-proxy", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.BoolVar(&opts.checkConfig, "check-config", false, "validate configuration and exit without starting the relay")
	set.StringVar(&configPath, "config", configPath, "JSON configuration file")
	set.StringVar(&opts.socksListen, "listen", opts.socksListen, "SOCKS5 listen address")
	set.StringVar(&opts.httpListen, "http-listen", opts.httpListen, "optional HTTP proxy listen address (CONNECT and plain HTTP)")
	set.StringVar(&opts.relayExe, "relay-exe", opts.relayExe, "Windows relay executable")
	set.BoolVar(&opts.brokerMode, "broker-mode", opts.brokerMode, "reuse one relay client across reconnecting broker connector processes")
	set.StringVar(&opts.upstreamProxy, "upstream-proxy", opts.upstreamProxy, "optional Windows-side HTTP CONNECT or SOCKS5 proxy URL")
	set.Var(&opts.reverse, "reverse", "reverse mapping WINDOWS_ADDR=WSL_TARGET (repeatable)")
	set.Var(&opts.reverseUDP, "reverse-udp", "reverse UDP mapping WINDOWS_ADDR=WSL_TARGET (repeatable)")
	set.BoolVar(&opts.autoForward, "auto-forward", opts.autoForward, "automatically mirror WSL TCP listeners to Windows")
	set.BoolVar(&opts.autoForwardUDP, "auto-forward-udp", fileConfig.AutoForward.UDPEnabled, "opt-in UDP listener mirroring; requires an explicit allowlist")
	set.StringVar(&opts.autoForwardHost, "auto-forward-host", opts.autoForwardHost, "Windows bind host for automatic mappings")
	set.StringVar(&opts.autoForwardHost6, "auto-forward-host6", opts.autoForwardHost6, "Windows IPv6 bind host for automatic mappings")
	set.IntVar(&opts.autoForwardPortOffset, "auto-forward-port-offset", opts.autoForwardPortOffset, "add this offset to Windows ports for automatic mappings")
	set.BoolVar(&opts.autoForwardPortAuto, "auto-forward-port-auto", opts.autoForwardPortAuto, "let Windows allocate ports for automatic mappings")
	set.StringVar(&opts.autoForwardStatus, "auto-forward-status", opts.autoForwardStatus, "optional JSON status file for active automatic mappings")
	set.DurationVar(&opts.autoForwardInterval, "auto-forward-interval", opts.autoForwardInterval, "automatic listener scan interval")
	set.DurationVar(&opts.autoRetryMin, "auto-forward-retry-min", opts.autoRetryMin, "minimum delay after an automatic mapping refusal")
	set.DurationVar(&opts.autoRetryMax, "auto-forward-retry-max", opts.autoRetryMax, "maximum delay after repeated automatic mapping refusals")
	set.StringVar(&include, "auto-forward-include", include, "comma-separated allowlist of ports for automatic mapping")
	set.StringVar(&udpInclude, "auto-forward-udp-include", udpInclude, "comma-separated allowlist of UDP ports for automatic mapping")
	set.StringVar(&exclude, "auto-forward-exclude", exclude, "comma-separated ports excluded from automatic mapping")
	set.StringVar(&opts.controlSocket, "control-socket", opts.controlSocket, "Unix socket for strict listener coordination; empty disables")
	set.StringVar(&opts.strictListenHost, "strict-listen-host", opts.strictListenHost, "Windows bind host for strict listener coordination")
	set.StringVar(&opts.strictListenHost6, "strict-listen-host6", opts.strictListenHost6, "Windows IPv6 bind host for strict listener coordination")
	set.DurationVar(&opts.udpAssociateIdle, "udp-associate-idle-timeout", opts.udpAssociateIdle, "idle timeout for SOCKS5 UDP associations")
	set.DurationVar(&opts.relayHandshakeTimeout, "relay-handshake-timeout", opts.relayHandshakeTimeout, "maximum time to wait for the Windows relay handshake")
	set.DurationVar(&opts.relayDialTimeout, "relay-dial-timeout", opts.relayDialTimeout, "maximum time to wait for a relay session to open a connection")
	set.DurationVar(&opts.proxyHandshakeTimeout, "proxy-handshake-timeout", opts.proxyHandshakeTimeout, "maximum time for a SOCKS5 or HTTP CONNECT client handshake")
	if err := set.Parse(args); err != nil {
		return options{}, err
	}
	if opts.autoForwardInterval <= 0 {
		return options{}, errors.New("auto-forward-interval must be positive")
	}
	if opts.autoRetryMin <= 0 {
		return options{}, errors.New("auto-forward-retry-min must be positive")
	}
	if opts.autoRetryMax <= 0 {
		return options{}, errors.New("auto-forward-retry-max must be positive")
	}
	if opts.autoRetryMax < opts.autoRetryMin {
		return options{}, errors.New("auto-forward-retry-max must be greater than or equal to auto-forward-retry-min")
	}
	if opts.autoForwardPortOffset < -65534 || opts.autoForwardPortOffset > 65534 {
		return options{}, errors.New("auto-forward-port-offset must be between -65534 and 65534")
	}
	if opts.autoForwardPortAuto && opts.autoForwardPortOffset != 0 {
		return options{}, errors.New("auto-forward-port-auto and auto-forward-port-offset cannot be used together")
	}
	if opts.udpAssociateIdle <= 0 {
		return options{}, errors.New("udp-associate-idle-timeout must be positive")
	}
	if opts.relayHandshakeTimeout <= 0 {
		return options{}, errors.New("relay-handshake-timeout must be positive")
	}
	if opts.relayDialTimeout <= 0 {
		return options{}, errors.New("relay-dial-timeout must be positive")
	}
	if opts.proxyHandshakeTimeout <= 0 {
		return options{}, errors.New("proxy-handshake-timeout must be positive")
	}
	if set.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(set.Args(), " "))
	}
	opts.autoInclude, err = parsePortSet(include)
	if err != nil {
		return options{}, fmt.Errorf("auto-forward-include: %w", err)
	}
	opts.autoUDPInclude, err = parsePortSet(udpInclude)
	if err != nil {
		return options{}, fmt.Errorf("auto-forward-udp-include: %w", err)
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
	if opts.brokerMode && opts.upstreamProxy != "" {
		return options{}, errors.New("upstream proxy must be configured on the Windows broker when broker mode is enabled")
	}
	if opts.autoForwardUDP {
		if !opts.autoForward {
			return options{}, errors.New("-auto-forward-udp requires -auto-forward")
		}
		if len(opts.autoUDPInclude) == 0 {
			return options{}, errors.New("-auto-forward-udp requires a non-empty -auto-forward-udp-include allowlist")
		}
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

func portsFromSet(ports map[uint16]bool) []uint16 {
	result := make([]uint16, 0, len(ports))
	for port, enabled := range ports {
		if enabled {
			result = append(result, port)
		}
	}
	return result
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
	dialer := newSessionDialer()
	logger.Printf("SOCKS5 listening on %s", socksListener.Addr())
	var control *listencontrol.Server
	var controlDone chan error
	if opts.controlSocket != "" {
		control = &listencontrol.Server{
			Path: opts.controlSocket, WindowsHost: opts.strictListenHost, WindowsHost6: opts.strictListenHost6,
			Reserve: func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
				return dialer.reserveReverseForward(reserveCtx, windows, wsl)
			},
			ReserveDatagram: func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
				closer, err := dialer.reserveDatagramForward(reserveCtx, windows, wsl)
				if err != nil {
					return nil, err
				}
				return noCommitReservation{Closer: closer}, nil
			},
		}
		controlDone = make(chan error, 1)
		go func() { controlDone <- control.Serve(ctx) }()
		logger.Printf("strict-listen control socket %s", opts.controlSocket)
	}
	if control != nil {
		go retryMissingLeases(ctx, opts.relayDialTimeout, logger, dialer, control)
	}
	socksDone := make(chan error, 1)
	proxy := &socks5.Server{Listener: socksListener, Dialer: dialer, Logger: logger, UDPAssociateIdleTimeout: opts.udpAssociateIdle, DialTimeout: opts.relayDialTimeout, HandshakeTimeout: opts.proxyHandshakeTimeout}
	go func() { socksDone <- proxy.Serve(ctx) }()
	var httpDone chan error
	if httpListener != nil {
		httpDone = make(chan error, 1)
		httpProxy := &httpproxy.Server{Listener: httpListener, Dialer: dialer, Logger: logger, DialTimeout: opts.relayDialTimeout, HandshakeTimeout: opts.proxyHandshakeTimeout}
		go func() { httpDone <- httpProxy.Serve(ctx) }()
		logger.Printf("HTTP proxy listening on %s (CONNECT and plain HTTP)", httpListener.Addr())
	}
	var autoDone chan error
	var autoUDPDone chan error
	if opts.autoForward {
		var autoStatus *autoforward.StatusStore
		if opts.autoForwardStatus != "" {
			autoStatus = autoforward.NewStatusStore(opts.autoForwardStatus)
		}
		excluded := clonePortSet(opts.autoExclude)
		addAddressPort(excluded, socksListener.Addr().String())
		if httpListener != nil {
			addAddressPort(excluded, httpListener.Addr().String())
		}
		for _, mapping := range opts.reverse {
			addAddressPort(excluded, mapping.Windows)
			addAutomaticMappedPort(excluded, mapping.Windows, opts.autoForwardPortOffset)
			addAddressPort(excluded, mapping.WSL)
		}
		for _, mapping := range opts.reverseUDP {
			addAddressPort(excluded, mapping.Windows)
			addAutomaticMappedPort(excluded, mapping.Windows, opts.autoForwardPortOffset)
			addAddressPort(excluded, mapping.WSL)
		}
		watcher := &autoforward.Watcher{Scanner: autoforward.DefaultProcScanner(), Opener: dialer, WindowsHost: opts.autoForwardHost, WindowsHost6: opts.autoForwardHost6, WindowsPortOffset: opts.autoForwardPortOffset, WindowsPortAuto: opts.autoForwardPortAuto, Status: autoStatus, StatusOwner: "tcp", Interval: opts.autoForwardInterval, OpenTimeout: opts.relayDialTimeout, RetryMin: opts.autoRetryMin, RetryMax: opts.autoRetryMax, Included: opts.autoInclude, Excluded: excluded, Logger: logger}
		autoDone = make(chan error, 1)
		go func() { autoDone <- watcher.Run(ctx) }()
		if opts.autoForwardPortAuto {
			logger.Printf("automatic forwarding enabled on Windows hosts %s (IPv4), %s (IPv6), Windows-allocated ports", opts.autoForwardHost, opts.autoForwardHost6)
		} else {
			logger.Printf("automatic forwarding enabled on Windows hosts %s (IPv4), %s (IPv6), port offset %d", opts.autoForwardHost, opts.autoForwardHost6, opts.autoForwardPortOffset)
		}
		var udpWatcher *autoforward.DatagramWatcher
		if opts.autoForwardUDP {
			udpWatcher = &autoforward.DatagramWatcher{Scanner: autoforward.DefaultProcScanner(), Opener: dialer, WindowsHost: opts.autoForwardHost, WindowsHost6: opts.autoForwardHost6, WindowsPortOffset: opts.autoForwardPortOffset, WindowsPortAuto: opts.autoForwardPortAuto, Status: autoStatus, Interval: opts.autoForwardInterval, OpenTimeout: opts.relayDialTimeout, RetryMin: opts.autoRetryMin, RetryMax: opts.autoRetryMax, Included: opts.autoUDPInclude, Excluded: excluded, Logger: logger}
			autoUDPDone = make(chan error, 1)
			go func() { autoUDPDone <- udpWatcher.Run(ctx) }()
			logger.Printf("automatic UDP forwarding enabled for allowlisted ports %s", formatPorts(portsFromSet(opts.autoUDPInclude)))
		}
		dialer.setClearHook(func() {
			watcher.Reset()
			if udpWatcher != nil {
				udpWatcher.Reset()
			}
		})
	}
	sessionDone := make(chan error, 1)
	go func() {
		if opts.brokerMode {
			sessionDone <- runPersistent(ctx, opts, logger, dialer, control)
			return
		}
		sessionDone <- supervise(ctx, opts, logger, func(sessionCtx context.Context, sessionOpts options, sessionLogger *log.Logger) error {
			return runSession(sessionCtx, sessionOpts, sessionLogger, dialer, control)
		}, relayRestartDelay)
	}()
	select {
	case err := <-sessionDone:
		return err
	case err := <-socksDone:
		return sessionCompletion(ctx, err)
	case err := <-httpDone:
		return sessionCompletion(ctx, err)
	case err := <-controlDone:
		return sessionCompletion(ctx, err)
	case err := <-autoDone:
		return sessionCompletion(ctx, err)
	case err := <-autoUDPDone:
		return sessionCompletion(ctx, err)
	case <-parent.Done():
		return parent.Err()
	}
}

func runSession(parent context.Context, opts options, logger *log.Logger, dialer *sessionDialer, control *listencontrol.Server) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	cmd := exec.CommandContext(ctx, opts.relayExe, relayArguments(opts)...)
	cmd.Stderr = os.Stderr
	if opts.brokerMode {
		cmd.Env = connectorEnvironment()
	}
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
		if cmd.Process != nil && !processWaited {
			_ = cmd.Process.Kill()
		}
		if !waitProcess(2 * time.Second) {
			logger.Printf("relay process did not exit promptly")
		}
	}()

	relayDone := make(chan error, 1)
	go func() { relayDone <- client.Run(ctx) }()
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, opts.relayHandshakeTimeout)
	capabilities, err := client.Handshake(handshakeCtx, requiredRelayCapabilities(opts))
	handshakeCancel()
	if err != nil {
		if waitProcess(100 * time.Millisecond) {
			if fatal := classifyRelayProcessExit(processErr); fatal != nil {
				return fatal
			}
		}
		return classifyHandshakeError(err)
	}
	dialer.set(client)
	defer dialer.clear(client)
	logger.Printf("Windows relay ready (capabilities 0x%x)", capabilities)
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
	if control != nil {
		rebindControl(ctx, opts.relayDialTimeout, logger, client, control)
	}

	select {
	case err := <-relayDone:
		err = sessionCompletion(ctx, err)
		if ctx.Err() != nil {
			return err
		}
		if waitProcess(100 * time.Millisecond) {
			if fatal := classifyRelayProcessExit(processErr); fatal != nil {
				return fatal
			}
		}
		if isRelayTransportExit(err) {
			return errRelayExited
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func requiredRelayCapabilities(opts options) uint64 {
	capabilities := protocol.CoreCapabilities
	if opts.autoForwardPortAuto {
		capabilities |= protocol.CapabilityListenBoundAddress
	}
	return capabilities
}

func rebindControl(ctx context.Context, timeout time.Duration, logger *log.Logger, client *relay.Client, control *listencontrol.Server) {
	if control == nil {
		return
	}
	rebindCtx, rebindCancel := context.WithTimeout(ctx, timeout)
	defer rebindCancel()
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
	if rebindErr != nil {
		logger.Printf("strict-listen lease rebind: %v", rebindErr)
	}
}

func retryMissingLeases(parent context.Context, timeout time.Duration, logger *log.Logger, dialer *sessionDialer, control *listencontrol.Server) {
	interval := timeout
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-ticker.C:
		}
		retryCtx, cancel := context.WithTimeout(parent, timeout)
		err := control.RetryMissing(retryCtx,
			func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
				return dialer.reserveReverseForward(reserveCtx, windows, wsl)
			},
			func(reserveCtx context.Context, windows, wsl string) (listencontrol.Reservation, error) {
				closer, reserveErr := dialer.reserveDatagramForward(reserveCtx, windows, wsl)
				if reserveErr != nil {
					return nil, reserveErr
				}
				return noCommitReservation{Closer: closer}, nil
			},
		)
		cancel()
		if err != nil && parent.Err() == nil {
			logger.Printf("strict-listen missing lease retry: %v", err)
		}
	}
}

func isRelayTransportExit(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe)
}

func classifyRelayProcessExit(err error) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil || !exitErr.ProcessState.Exited() || exitErr.ExitCode() == 0 {
		return nil
	}
	return fmt.Errorf("Windows relay exited with status %d: %w", exitErr.ExitCode(), err)
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

func connectorEnvironment() []string {
	env := os.Environ()
	wslenv := os.Getenv("WSLENV")
	entries := normalizeWSLENV(strings.Split(wslenv, ":"), []string{"WSL_WIN_RELAY_BROKER_ENDPOINT", "WSL_WIN_RELAY_ATTACH_TOKEN"})
	value := "WSLENV=" + strings.Join(entries, ":")
	for index, entry := range env {
		if strings.HasPrefix(entry, "WSLENV=") {
			env[index] = value
			return env
		}
	}
	return append(env, value)
}

func normalizeWSLENV(entries, required []string) []string {
	requiredSet := make(map[string]bool, len(required))
	for _, name := range required {
		requiredSet[name] = true
	}
	seen := make(map[string]bool, len(required))
	result := make([]string, 0, len(entries)+len(required))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		base := entry
		if slash := strings.IndexByte(entry, '/'); slash >= 0 {
			base = entry[:slash]
		}
		if requiredSet[base] {
			if !seen[base] {
				result = append(result, base)
				seen[base] = true
			}
			continue
		}
		result = append(result, entry)
	}
	for _, name := range required {
		if !seen[name] {
			result = append(result, name)
		}
	}
	return result
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

func addAutomaticMappedPort(set map[uint16]bool, windowsAddress string, offset int) {
	if offset == 0 {
		return
	}
	_, rawPort, err := net.SplitHostPort(windowsAddress)
	if err != nil {
		return
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		return
	}
	sourcePort := port - offset
	if sourcePort >= 1 && sourcePort <= 65535 {
		set[uint16(sourcePort)] = true
	}
}
