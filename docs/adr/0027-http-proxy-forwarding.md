# ADR-0027: HTTP Proxy Request Modes

## Status

Accepted

## Context

The HTTP frontend originally implemented only CONNECT, which is sufficient for
HTTPS but rejects ordinary cleartext URLs sent by clients using `HTTP_PROXY`.
Those clients are common in command-line workflows, and the Windows-side
dialer already provides the correct egress and DNS boundary for an origin
connection.

## Decision

Keep CONNECT as a byte tunnel and add one-request absolute-form forwarding for
plain HTTP:

- CONNECT requires the existing `host:port` target and preserves buffered bytes
  after the handshake.
- Non-CONNECT requests must use an absolute `http://` URL. The frontend derives
  the origin target from its hostname and optional port (default `80`), then
  sends the request in origin form through the relay dialer.
- Proxy-only `Proxy-Connection` and `Proxy-Authorization` headers are removed.
  The request is marked `Connection: close`, so one client connection carries
  one origin request and response. This bounds lifecycle state without adding a
  second HTTP session multiplexer.
- Absolute `https://` requests and relative-form requests are rejected with
  `400`; clients must use CONNECT for TLS. Dial failures return `502`.
- The existing 64 KiB header and handshake deadlines apply before dispatch;
  request bodies continue to stream through `net/http` without an additional
  fixed-size buffer.

## Consequences

HTTP clients can use the same listener for both `HTTP_PROXY` and
`HTTPS_PROXY`. The implementation remains loopback-oriented, does not expose a
general-purpose unauthenticated LAN proxy, and leaves HTTP keep-alive and
multi-request pooling to a future explicitly designed session layer.
