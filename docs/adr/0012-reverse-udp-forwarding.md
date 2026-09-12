# ADR-0012: Add Explicit Reverse UDP Forwarding

## Status
Accepted; automatic-discovery boundary superseded by [ADR-0013](0013-allowlisted-udp-discovery.md)

## Context

The relay already supports outbound UDP and reverse TCP listeners. A WSL UDP
service still cannot be reached from a Windows client because the Windows side
has no listener that carries datagram source metadata across the multiplexed
transport. UDP also cannot reuse reverse TCP streams without losing message
boundaries and endpoint identity.

## Decision

Add a dedicated reverse UDP listener lifecycle and datagram frame family. The
Windows side binds the configured UDP address and sends each received packet as
an endpoint-preserving frame. The WSL side maintains one ephemeral local UDP
socket per Windows source endpoint, writes incoming data to the configured WSL
target, and sends responses back with the original Windows endpoint. Closing a
listener closes all per-source flows.

Expose explicit mappings through `reverse_udp` configuration entries and the
repeatable `-reverse-udp WINDOWS_ADDR=WSL_TARGET` flag. Bind failures are
returned during mapping registration just like reverse TCP failures.

At the time of this decision, do not include UDP sockets in unrestricted
automatic `/proc` polling. Linux proc UDP tables do not reliably identify
which bound sockets are servers versus ephemeral client sockets, so mapping
every row would create surprising Windows listeners and port conflicts. The
later allowlisted adapter is defined separately in ADR-0013; this decision
still governs the explicit reverse UDP protocol and its endpoint-preserving
flow model.

## Consequences

### Positive

- Windows clients can reach WSL UDP services with preserved source identity.
- Multiple concurrent Windows sources do not share a single ambiguous response
  route.
- TCP reverse forwarding and outbound UDP semantics remain unchanged.
- The explicit-only boundary avoids unsafe UDP listener discovery.

### Negative

- Each active source endpoint consumes one local ephemeral UDP socket and flow;
  idle flows expire after five minutes.
- Unrestricted UDP port discovery remains unsupported; the allowlisted
  polling adapter intentionally accepts a documented procfs ambiguity.
- UDP firewall policy after bind still cannot be represented as a WSL bind
  failure.
