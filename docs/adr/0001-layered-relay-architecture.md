# ADR-0001: Layer the Relay Around a Versioned Multiplexed Protocol

## Status
Accepted

## Context

WSL networking can fail while Windows networking remains usable. The recovery path must therefore cross the WSL/Windows boundary without depending on HNS, a WSL virtual NIC, or a Windows loopback connection. WSL interop can launch a Windows executable, and the executable's standard input/output can carry arbitrary bytes.

The project is expected to grow from a SOCKS5 emergency proxy toward transparent TCP/UDP adapters. A one-off SOCKS implementation or file-specific transport would make later work expensive and difficult to test.

## Decision

Use four layers:

1. **Protocol:** versioned multiplexed frames with bounded payloads and explicit stream lifecycle.
2. **Transport:** a byte-stream abstraction, initially Windows process stdin/stdout.
3. **Relay:** stream-oriented `net.Conn` semantics and Windows outbound dialing.
4. **Adapters:** SOCKS5 first; transparent TCP, UDP, DNS, and TUN later.

The first transport is process pipes, not shared memory. The first adapter is loopback-only SOCKS5 with no authentication.

## Consequences

### Positive

- Protocol and relay behavior can be tested without WSL or HNS.
- Future transports can reuse stream semantics.
- Future adapters do not need to alter Windows dialing logic.
- Process pipes avoid polling and shared-file cleanup.

### Negative

- Pipe throughput and buffering need explicit backpressure handling.
- A crashed Windows child disconnects all active streams.
- SOCKS5 does not transparently cover UDP or applications without proxy support.
- A custom protocol requires compatibility and security discipline.

## Alternatives Considered

**Shared files:** Proven by File-Tunnel, but higher latency and more filesystem failure modes for a local same-machine path.

**Shared memory:** Not directly available across the WSL2 VM boundary as ordinary user memory; mapped files add synchronization complexity without removing the need for a framing protocol.

**Named pipes or AF_VSOCK first:** Potential future transports, but platform availability and WSL behavior vary more than standard process pipes.

## References

- https://github.com/fiddyschmitt/File-Tunnel
