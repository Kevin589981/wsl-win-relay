# WSL-Windows Relay Implementation Plan

**Goal:** Build a durable emergency network relay that exposes a SOCKS5 proxy in WSL while a Windows process performs outbound TCP connections.

**Architecture:** A versioned multiplexed frame protocol runs over the Windows process stdin/stdout pipes. The WSL process owns the local proxy listener and relay client; the Windows process owns outbound sockets and relay server state. Protocol, transport, relay, and proxy layers are separate so later transparent TCP/UDP adapters can reuse the relay.

**Tech Stack:** Go standard library, cross-compiled Windows/Linux binaries, `go test`.

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
