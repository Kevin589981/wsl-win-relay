# ADR-0009: Use tun2socks for Transparent WSL Routing

## Status
Accepted

## Context

SOCKS5 and HTTP CONNECT require application proxy support. The requested end state also includes applications that do not understand proxies, plus UDP and QUIC. Reimplementing a user-space TCP/IP stack in this project would create a second networking implementation with a large maintenance and security surface.

## Decision

Integrate the released `xjasonlyu/tun2socks` v2.7.0 binary as the transparent adapter. `scripts/transparent-relay.sh` creates an isolated TUN device, routes IPv4 (and IPv6 when available) through it, starts tun2socks against the local SOCKS5 endpoint, optionally replaces DNS with a chosen resolver, and restores every route/DNS/device change on exit.

The relay transport itself remains independent of TUN. The Windows process still performs TCP and UDP egress, including DNS resolution for SOCKS domain requests.

## Consequences

### Positive

- Proxy-unaware TCP and UDP applications can use the same Windows egress.
- Mature gVisor networking handles TCP state, UDP, IPv6, and packet boundaries.
- Route and DNS changes have explicit rollback traps.

### Negative

- Transparent mode requires root, `/dev/net/tun`, `iproute2`, and an installed tun2socks binary.
- A route or DNS change made outside the script is not owned or restored by it.
- The local SOCKS endpoint must remain reachable outside the TUN route; the default loopback endpoint satisfies this.
- Kernel features, static binaries, and applications that bypass normal routing remain platform-specific concerns.

## References

- https://github.com/xjasonlyu/tun2socks/releases/tag/v2.7.0
- https://github.com/xjasonlyu/tun2socks/wiki/Examples
