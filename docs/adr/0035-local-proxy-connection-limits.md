# ADR-0035: Bound Local Proxy Client Connections

## Status

Accepted

## Context

SOCKS5 and HTTP handshake deadlines reclaim idle clients, but the shared accept
loop previously created an unbounded connection entry and handler goroutine for
every accepted loopback client. A burst within the timeout window could exhaust
descriptors or memory before those deadlines fired, independently of relay
stream limits.

## Decision

The shared `netserve` accept loop enforces a maximum number of active handlers.
SOCKS5 and HTTP each default to 256 concurrent local clients and receive the
same validated `max_proxy_connections` / `-max-proxy-connections` setting.
Accepted connections beyond that per-listener limit are closed immediately
without starting a handler or writing a log entry. When a handler exits, its
slot becomes available automatically.

The valid configuration range is `1..65535`. Existing handlers are never
evicted to make room for new clients, and shutdown still closes and drains all
accepted connections.

## Consequences

- Idle-handshake bursts have a fixed descriptor and goroutine bound.
- SOCKS5 and HTTP limits are independent, so enabling HTTP cannot starve the
  primary SOCKS5 listener completely.
- Rejections are intentionally silent to avoid log amplification under load.
- Relay registry limits remain a separate layer for established proxy and
  reverse-forward objects.
