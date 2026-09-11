# ADR-0002: Use SOCKS5 as the First User-Facing Adapter

## Status
Accepted

## Context

The relay needs an immediately useful interface while transparent routing is still being designed. SOCKS5 supports TCP CONNECT, domain-name dialing, and broad command-line client support without requiring routing-table or kernel interception changes.

## Decision

Implement a no-auth SOCKS5 CONNECT listener bound to `127.0.0.1` by default. Domain names are sent to Windows for resolution by using the relay's target string rather than resolving in WSL.

## Consequences

### Positive

- Useful for `curl`, Git, package managers, and other proxy-aware tools.
- DNS can occur on the Windows side, avoiding broken WSL DNS.
- The adapter is small and independently testable.

### Negative

- Programs without proxy support need wrappers or a later transparent adapter.
- UDP, QUIC, and raw DNS are not covered by the first release.
- No-auth is safe only when the listener remains loopback-only.

## Alternatives Considered

**Transparent TUN immediately:** Broader coverage, but requires routing, DNS, UDP, IPv6, and privilege work before the core IPC path is proven.

**HTTP CONNECT only:** Simpler for some clients, but less general than SOCKS5 and does not provide a good base for later non-HTTP adapters.
