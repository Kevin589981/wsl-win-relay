# WSL-Windows Relay Implementation Plan

**Goal:** Build a durable emergency network relay that exposes a SOCKS5 proxy in WSL while a Windows process performs outbound TCP connections.

**Architecture:** A versioned multiplexed frame protocol runs over the Windows process stdin/stdout pipes. The WSL process owns the local proxy listener and relay client; stdio mode keeps relay state in the Windows child, while broker mode moves it into a persistent socket owner behind replaceable frontend, bridge-worker, and socket-host bridge processes. Protocol, transport, relay, and proxy layers are separate so later transparent TCP/UDP adapters can reuse the relay.

**Tech Stack:** Go standard library, cross-compiled Windows/Linux binaries, `go test`.

## Current Completion Matrix

- [x] Framed stdio relay with bounded streams and capability negotiation.
- [x] SOCKS5 TCP CONNECT and UDP ASSOCIATE, plus optional HTTP CONNECT.
- [x] Windows-side HTTP CONNECT and SOCKS5/SOCKS5H upstream support.
- [x] Explicit TCP and UDP reverse forwarding with bind-error propagation.
- [x] Polling TCP listener discovery with include/exclude policies.
- [x] Strict TCP `listen()` and non-zero UDP `bind()` coordination through the
      native launcher/interposer, including descriptor ownership cleanup.
- [x] Direct `syscall(SYS_listen/SYS_bind)` coordination for dynamically linked
      Linux amd64 applications.
- [x] Lease adoption for process-style `clone()` children, in addition to
      ordinary `fork()`.
- [x] Parent-side lease adoption for dynamically linked raw `SYS_clone` and
      `SYS_clone3` process children.
- [x] Explicitly allowlisted UDP polling for common unconnected services, with
      a documented procfs ambiguity boundary.
- [x] Process-boundary recovery that recreates mappings after broken relay
      transports.
- [x] Opt-in TUN/tun2socks transparent TCP/UDP routing, including real WSL
      verification while HNS had removed the default route.
- [x] systemd user-service installer and private configuration handling.
- [x] Independent IPv4/IPv6 Windows bind hosts for automatic and strict
      listener mappings.
- [x] Session-independent local SOCKS5/HTTP listeners with a reconnecting
      dialer for new requests during relay restarts.
- [x] Strict control socket and live listener leases survive relay child
      replacement; Windows reservations are rebound and recommitted for the
      replacement session.
- [x] Automatic TCP and allowlisted UDP mappings are process-scoped and
      rebound through the session-aware dialer after relay child replacement.
- [x] Generation-safe attach registry with per-instance token authentication
      and stale connector teardown isolation (transport foundation for the
      persistent Windows broker).
- [x] Versioned attach-control wire handshake with bounded framing,
      capability exchange, token authentication, and generation acknowledgements.
- [x] Stable registry-entry summary and resume-ack codecs with deterministic
      ordering and bounded entry counts.
- [x] Attach/resume handshake flow exchanges the summary and validates the
      connector's epoch and acknowledged stable IDs before session activation.
- [x] Per-user local IPC transport abstraction with `0600` Unix-socket
      enforcement and Windows named-pipe security descriptor.
- [x] Transport-independent broker core with stable entry registration,
      summary snapshots, attach/resume acceptance, and stale-session isolation.
- [x] Broker listener lifecycle with active connector tracking, cancellation
      teardown, and handler draining before service return.
- [x] Opt-in broker and connector executables bridge stdio to local IPC and
      run the existing relay server after attach; offline WSL SOCKS5 integration
      is covered by `scripts/test-broker-connector.sh`.
- [x] Real WSL-to-Windows broker interop smoke builds the Windows binaries,
      exports broker credentials through `WSLENV`, attaches over named pipes,
      and reaches an external HTTPS endpoint.
- [x] Frame-aware replaceable Link and `relay.Server.ServeAttached` keep the
      broker server lifecycle alive across connector transport replacement.
- [x] Relay client `RunAttached`/`Rehandshake` reuse the same stream registry,
      and retry detach-sensitive frame writes; in-memory TCP streams survive
      both-end transport replacement.
- [x] Non-zero normal Windows relay exit status is classified as fatal instead
      of causing an unbounded restart loop.
- [x] Parent-side `vfork()` lease adoption is covered by the native lifecycle
      smoke test; child-side pre-exec networking remains unsupported by the
      shared-address-space contract.
- [ ] Kernel-level coverage for static binaries and unusual thread-group
      ownership patterns; static/setuid binaries remain outside LD_PRELOAD.
- [x] Opt-in broker-mode hot reconnect preserves existing TCP streams across
      connector/stdio replacement; a real delayed HTTP stream test covers the
      connector process boundary.
- [x] Broker-mode keeps an already-bound reverse listener usable through
      connector replacement; the integration test covers a new accepted stream
      after reconnect.
- [x] Broker-mode preserves an allowlisted reverse-UDP flow through connector
      replacement; the integration test verifies the response path.
- [x] Broker-mode preserves a procfs-discovered automatic WSL listener mapping
      through connector replacement; the integration test exercises the
      Windows-facing listener after reconnect.
- [x] Automatic TCP/UDP watcher mappings are generation-scoped, so an opener
      that completes after relay reset cannot republish a stale-session mapping.
- [x] Automatic mapping opens use a bounded worker pool so one slow Windows
      bind/relay request cannot serialize an entire listener scan.
- [x] The bounded mapping worker pool is covered by a concurrency regression
      test and keeps stale-session cleanup checks in every worker.
- [x] Relay reset and watcher shutdown cancel in-flight mapping attempts so a
      reconnect does not wait for the full per-port open timeout.
- [x] Initial session attachment does not reset freshly-created automatic
      mappings, and pending reverse listener OPEN/CLOSE races cancel before a
      Windows socket can be published.
- [x] Automatic mapping reset is driven directly by session clear callbacks,
      so rapid broker peer clear/set transitions cannot lose the reset event.
- [x] Strict lease rebind releases stale peer reservations before replacement
      binds, preserving leases while preventing stale Windows EADDRINUSE.
- [x] Automatic mapping refusals use bounded exponential retry backoff instead
      of issuing a Windows bind attempt on every procfs scan; session resets
      clear the backoff immediately.
- [x] Broker-mode can be selected from the persistent JSON configuration while
      attach credentials remain environment-only.
- [x] Optional systemd user broker unit, private token file, and bounded broker
      process restart wrapper are available; socket-owner crashes remain the
      documented established-socket failure boundary.
- [x] Broker instance identity detects a broker process restart and rebuilds
      stale peer state plus explicit/automatic mappings without restarting the
      WSL proxy; a real broker-restart integration test covers both directions.
- [x] Broker-mode becomes the proxy service default when the broker installer
      provisions `WSL_WIN_RELAY_BROKER_MODE=1` in the private environment; JSON
      and command-line configuration remain available for explicit control.
- [x] Process-isolated broker frontend/bridge-worker/socket-host/socket-owner
      design is documented in ADR-0016; only the socket owner owns relay state.
- [x] Frontend, bridge-worker, and socket-host bridge crash recovery preserves
      established socket-owner-owned sockets and automatic/explicit/strict
      mappings; the integration test covers live delayed streams across all
      outer bridge force-kills.
- [x] Broker systemd unit uses `KillMode=process` so frontend restart does not
      terminate the bridge worker, socket-host bridge, or socket owner; normal stop uses cascading
      private control endpoints.
- [x] Frontend and bridge-worker supervisors continuously reap children they
      start and keep externally-owned roles reusable without claiming their
      lifecycle.
- [x] Socket-owner health probes detect a dead reused owner and rebuild the
      owner plus automatic/explicit/strict mappings; established streams end at
      the socket-owner crash boundary.
- [x] Broker connector credentials are propagated through a normalized,
      flag-free `WSLENV` entry; the contract is documented in ADR-0017.
- [x] Private worker/socket-host health probes are token-bound and reject stale
      same-endpoint processes owned by a different broker identity.
- [x] A confirmed token-mismatched role is stopped and drained before endpoint
      reuse, while transient probe failures leave externally-owned roles alone.
- [x] Windows broker interop smoke can route through a Windows-side upstream
      proxy without requiring WSL to reach that proxy endpoint.
- [x] Socket-host bridge crash recovery preserves already-established streams
      because the socket owner, rather than the bridge, owns the kernel sockets.
- [x] Socket-owner crash recovery rebuilds the owner, outer bridges, and new
      mappings; established streams remain the documented unrecoverable boundary.
- [x] Broker executable provides a host-level `-supervise` parent that restarts
      a crashed frontend with bounded backoff; the broker user-service wrapper
      uses it by default while preserving status-2 configuration failures.
- [x] Broker supervisor and internal roles accept a protected `-token-file` and
      propagate its path instead of exposing token contents in child arguments;
      legacy environment and `-token-hex` paths remain supported.
- [x] Broker connector retries transient local-IPC endpoint and handshake
      outages for a bounded window so WSL proxy services survive supervised
      frontend replacement; authentication/configuration failures remain
      terminal.
- [x] Optional Windows Task Scheduler installation provides a host-level
      `-supervise` boundary that can keep the broker available across WSL VM or
      user-service shutdown; established streams after a socket-owner crash
      remain intentionally unrecoverable.
- [x] New systemd broker installations create a protected `attach.token` and
      pass its path to the Windows broker while retaining legacy environment
      token compatibility for existing deployments.

The hot-reconnect implementation follows [ADR-0015](../adr/0015-persistent-windows-ownership-and-attach.md) and [ADR-0016](../adr/0016-process-isolated-socket-owner.md): relay state and socket ownership now live in an independent socket owner behind replaceable connector bridges.

---

### Task 1: Establish repository and design contracts

**Files:** `README.md`, `docs/adr/0001-layered-relay-architecture.md`, `docs/plans/2026-09-12-wsl-win-relay.md`

Write the supported first release, non-goals, security assumptions, process lifecycle, and protocol layering. Record why process pipes are the initial transport and why shared memory/TUN are deferred adapters rather than foundations.

Verify that the documents describe a bounded frame size, explicit stream lifecycle, and no-auth loopback-only SOCKS5 default.

### Task 2: Implement the framed protocol

**Files:** `internal/protocol/frame.go`, `internal/protocol/frame_test.go`

Implement fixed header encoding/decoding with magic, protocol version, frame type, stream ID, and payload length. Reject malformed headers, unknown types, oversized payloads, and truncated frames. Keep writes atomic through a caller-provided serialized writer.

Run `go test ./internal/protocol -v`.

### Task 3: Implement stdio transport

**Files:** `internal/transport/stdio/stdio.go`, `internal/transport/stdio/stdio_test.go`

Provide a small `io.ReadWriter`/`io.Closer` wrapper for process stdin/stdout and in-memory tests. Keep stderr outside the binary data path.

Run `go test ./internal/transport/stdio -v`.

### Task 4: Implement multiplexed relay client and server

**Files:** `internal/relay/client.go`, `internal/relay/server.go`, `internal/relay/relay_test.go`

Implement stream open/data/close/error semantics, bounded per-stream buffering, serialized frame writes, cancellation, and clean shutdown. Expose a `net.Conn`-compatible client stream and a server dialer interface. Add an in-memory end-to-end test with a local TCP echo service.

Run `go test ./internal/relay -race -v`.

### Task 5: Implement SOCKS5 adapter

**Files:** `internal/socks5/server.go`, `internal/socks5/server_test.go`

Implement SOCKS5 no-auth negotiation and CONNECT for IPv4, IPv6, and domain targets. Use the relay client as a dialer, map connection errors to protocol replies, and proxy bytes in both directions. Bind only to loopback by default.

Run `go test ./internal/socks5 -race -v`.

### Task 6: Add production entrypoints and operator documentation

**Files:** `cmd/wsl-proxy/main.go`, `cmd/win-relay/main.go`, `README.md`, `docs/adr/0002-socks5-first-adapter.md`

Add `wsl-proxy` flags for listener address and Windows relay executable, process signal handling, stderr logging, and `win-relay` server startup. Document build commands, WSL usage, environment variables, diagnostics, and security restrictions.

Run `go test ./...`, build Linux and Windows binaries, and run a local Windows-side relay integration check where available.

### Task 7: Cross-environment verification

Clone the repository in WSL, build the Linux proxy and Windows relay, exercise `curl` through SOCKS5, and record any interop or path quoting issues. Keep this verification separate from protocol unit tests so WSL/HNS failures are distinguishable from application failures.

### Task 8: Reverse port forwarding

Extend the protocol with listener registration and inbound stream frames. Add a Windows listener manager and a WSL mapping that dials a configured local destination for each accepted connection. Return Windows bind failures to the WSL process and preserve half-close semantics for forwarded connections. Keep automatic discovery of arbitrary application listeners out of this task; it requires a separate transparent interception adapter.

### Task 9: Automatic WSL listener discovery

Poll `/proc/net/tcp` and `/proc/net/tcp6`, normalize dual-stack listeners, and dynamically reconcile Windows reverse forwards. Provide safe loopback defaults, include/exclude policies, cleanup, retry after Windows rejection, parser tests, and a real WSL-to-Windows lifecycle check.

### Task 10: Strict synchronized listen mode

Design an opt-in launcher/interposition path that registers the Windows listener before the application observes `listen(2)` success. Return the Windows bind error to the application when registration fails. Document unsupported static/setuid binaries and keep polling discovery as the compatibility fallback.

### Task 11: UDP relay and SOCKS5 UDP ASSOCIATE

Add endpoint-preserving datagram frames and Windows UDP socket lifecycle management. Then implement SOCKS5 UDP ASSOCIATE, including domain destinations, source endpoint responses, association ownership, timeouts, and malformed or fragmented packet rejection.

### Task 12: Transparent WSL routing

Integrate a pinned tun2socks release as an external TUN adapter. Provide route/DNS/device setup with complete rollback, preserve an uplink route for the local relay endpoint, document root and `/dev/net/tun` requirements, and verify a proxy-unaware TCP/UDP application through Windows egress.

### Task 13: Long-running service integration

Provide a systemd user unit and idempotent installer. Restart the proxy after child-process failure, preserve private configuration permissions, and verify that startup handshake and explicit mappings are recreated after a restart while strict listener leases are rebound when the control socket remains alive.
