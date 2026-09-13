# ADR-0033: Cancel UDP Resolution With Relay Object Lifetimes

## Status

Accepted

## Context

UDP frames carry endpoint strings so Windows can perform DNS when WSL itself
has no network. The relay previously used `net.ResolveUDPAddr`, which has no
context parameter. A stalled system resolver could therefore keep a datagram
worker alive after its SOCKS association or reverse mapping had closed.

The context passed to mapping setup is also intentionally short-lived. Reusing
it after a successful registration would make later inbound datagrams fail as
soon as the setup timeout was canceled.

## Decision

Resolve UDP endpoints through a shared context-aware helper built on
`net.Resolver.LookupIPAddr`. It preserves numeric IPv4, IPv6, zone, and empty
bind-host behavior and keeps the protocol's existing numeric-port contract.

Each forward and reverse UDP object owns a context independent of its setup
timeout. Closing or invalidating that object cancels its context before closing
sockets. Windows native UDP, WSL reverse UDP, and SOCKS5 upstream UDP relay
address resolution all use context-aware resolution. Resolver cancellation is
normalized to `ctx.Err()` across operating systems.

## Consequences

- DNS failure cannot retain a UDP relay worker beyond its owning object.
- Setup timeouts still bound registration without shortening the mapping's
  successful lifetime.
- Literal endpoints avoid DNS and preserve their address-family checks.
- The relay wire format and capability set do not change.
