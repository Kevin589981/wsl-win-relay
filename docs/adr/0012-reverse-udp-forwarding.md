# ADR-0012: Add Explicit Reverse UDP Forwarding

## Status
Accepted

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

Do not include UDP sockets in automatic `/proc` polling yet. Linux proc UDP
tables do not reliably identify which bound sockets are servers versus
ephemeral client sockets, so automatic mirroring would create surprising
Windows listeners and port conflicts.

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
- Automatic UDP port discovery remains a future feature requiring stronger
  socket classification or an application-level registration path.
- UDP firewall policy after bind still cannot be represented as a WSL bind
  failure.
