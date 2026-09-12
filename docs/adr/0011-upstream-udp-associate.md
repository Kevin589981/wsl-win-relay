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
datagram. Native UDP sockets need a resolved `net.UDPAddr`. SOCKS5 has two
established DNS modes: `socks5` resolves names on the relay host, while
`socks5h` preserves a domain endpoint for the upstream proxy to resolve.

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
Native packet connections continue to resolve datagram destinations on Windows.
For a `socks5` upstream, TCP and UDP domain targets are resolved on Windows
before encoding; `socks5h` keeps them as domain targets for remote resolution.
The packet wrapper therefore retains a target-string write path without
forcing one DNS policy on both schemes.

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
- Native and SOCKS5 packet paths use different DNS policies: native UDP is
  resolved by Windows, plain SOCKS5 resolves locally on Windows, and SOCKS5H
  can resolve domain destinations remotely.

## Alternatives Considered

**Tunnel UDP over repeated HTTP CONNECT streams:** rejected because it is
non-standard, inefficient, and incompatible with ordinary HTTP proxies.

**Resolve every datagram name before the packet wrapper:** rejected because it
would prevent SOCKS5H remote resolution when Windows DNS is unavailable; the
choice is made from the explicit upstream scheme instead.

**Keep UDP native for every upstream scheme:** rejected because SOCKS5 UDP
ASSOCIATE is specifically useful when native Windows UDP egress is unavailable.
