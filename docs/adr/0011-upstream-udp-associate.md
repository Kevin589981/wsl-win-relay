# ADR-0011: Route Relay UDP Through SOCKS5 UDP ASSOCIATE

## Status
Accepted

## Context

The Windows relay can optionally use an upstream proxy when Windows itself
cannot reach the destination directly. TCP streams already support HTTP
CONNECT and SOCKS5. UDP datagrams need a distinct protocol: HTTP CONNECT has
no portable datagram mode, while SOCKS5 defines UDP ASSOCIATE and keeps a TCP
control connection alive for the association lifetime.

The relay protocol already carries a destination endpoint with every UDP
datagram. The Windows side resolves that endpoint before writing to its
`PacketConn`, so the upstream packet implementation can use the same address
contract as native UDP without changing WSL-side SOCKS5 behavior.

## Decision

Inject a packet-connection factory into the Windows relay server. The upstream
dialer implements the factory as follows:

- no upstream proxy: bind a native Windows UDP socket;
- HTTP/HTTPS upstream: bind native Windows UDP because CONNECT only proxies
  TCP;
- SOCKS5/SOCKS5H upstream: open a TCP control connection, negotiate
  authentication, issue UDP ASSOCIATE, and wrap a local UDP socket that
  encapsulates and decapsulates SOCKS5 UDP packets.

The SOCKS5 TCP control connection is closed together with the packet
connection, and context cancellation closes both so blocked reads terminate.
The relay continues to resolve datagram destinations on Windows before passing
them to the packet connection. This preserves existing behavior and avoids
introducing a second DNS policy in the upstream layer.

## Consequences

### Positive

- SOCKS5 upstreams cover both TCP proxy traffic and UDP DNS/datagrams.
- Existing direct and HTTP-proxy deployments retain their behavior.
- Packet transport is injected behind a small interface, keeping relay protocol
  logic independent of proxy implementation details.
- UDP association cleanup is deterministic on context cancellation and close.

### Negative

- HTTP/HTTPS upstreams cannot proxy UDP; users requiring that must use SOCKS5.
- SOCKS5 UDP support depends on the upstream proxy allowing UDP ASSOCIATE and
  receiving UDP traffic from the relay host.
- Destination names in the relay protocol are resolved by Windows before the
  upstream packet wrapper sees them, so `socks5h` does not provide a separate
  remote-DNS policy for relay datagrams.

## Alternatives Considered

**Tunnel UDP over repeated HTTP CONNECT streams:** rejected because it is
non-standard, inefficient, and incompatible with ordinary HTTP proxies.

**Resolve datagram names inside the SOCKS5 wrapper:** rejected because it would
split DNS behavior between relay paths and change the established protocol
contract.

**Keep UDP native for every upstream scheme:** rejected because SOCKS5 UDP
ASSOCIATE is specifically useful when native Windows UDP egress is unavailable.
