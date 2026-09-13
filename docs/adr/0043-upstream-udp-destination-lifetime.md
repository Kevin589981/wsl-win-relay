# ADR-0043: Bind Upstream UDP Resolution to Association Lifetime

## Status

Accepted

## Context

For a `socks5://` upstream, each UDP destination domain is resolved locally
before its datagram is encoded. This lookup used an independent background
context with a 30-second timeout. Closing the UDP association shut down its TCP
control connection and UDP socket but could not interrupt a lookup already in
progress, retaining the write worker until the timeout expired.

The context used to establish the SOCKS5 UDP association may itself be a short
setup timeout, so it cannot safely become the successful association's ongoing
lifetime.

## Decision

Give each successful SOCKS5 UDP packet connection its own cancelable lifetime
context. Local destination lookups derive their existing 30-second ceiling
from that context. `Close` cancels the lifetime before closing the control and
UDP sockets. The resolver is injectable on the private packet type for a
deterministic cancellation test.

The establishment context still closes the association if it is canceled
while `OpenPacketContext` is running, but it is not reused as the returned
object's independent lifetime.

## Consequences

- Closing a SOCKS5 UDP association immediately releases blocked destination
  DNS work.
- A short setup deadline cannot accidentally expire a healthy returned
  association.
- SOCKS5H behavior is unchanged because destination names continue to be sent
  to the upstream proxy without local resolution.
- Wire format, association timeout, and UDP framing remain unchanged.
