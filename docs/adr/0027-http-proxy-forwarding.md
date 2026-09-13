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

Keep CONNECT as a byte tunnel and add absolute-form forwarding for plain HTTP:

- CONNECT requires the existing `host:port` target and preserves buffered bytes
  after the handshake.
- Non-CONNECT requests must use an absolute `http://` URL. The frontend derives
  the origin target from its hostname and optional port (default `80`), then
  sends the request in origin form through the relay dialer.
- Proxy-only headers and all hop-by-hop headers named by `Connection` (plus the
  standard `Keep-Alive`, `TE`, `Trailer`, and `Upgrade` headers) are removed.
  Each request is sent with `Connection: close` to a fresh origin connection,
  while sequential requests may reuse the client-side proxy connection. Origin
  responses are parsed, have hop-by-hop headers removed, and are written back
  with the client's close semantics.
- The frontend does not send a relay half-close after writing the request. The
  complete HTTP message framing is sufficient for the origin, while avoiding
  intermediaries that interpret a FIN as termination of the entire tunnel.
- Absolute `https://` requests and relative-form requests are rejected with
  `400`; clients must use CONNECT for TLS. Dial failures return `502`.
- Every HTTP request header block is bounded at 64 KiB, including subsequent
  requests on a keep-alive client connection. The handshake deadline is applied
  while each request is parsed; request bodies continue to stream through
  `net/http` without an additional fixed-size buffer. Parser read-ahead is
  returned to the shared client stream before the next request is decoded.

## Consequences

HTTP clients can use the same listener for both `HTTP_PROXY` and
`HTTPS_PROXY`. The implementation remains loopback-oriented and does not
expose a general-purpose unauthenticated LAN proxy. It deliberately avoids
pooling origin connections, so one slow origin request cannot mix state with
another client request.
