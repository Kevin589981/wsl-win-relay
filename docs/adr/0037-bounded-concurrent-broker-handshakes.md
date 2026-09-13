# ADR-0037: Bound and Parallelize Broker Attach Handshakes

## Status

Accepted

## Context

The broker attach deadline bounded one connector handshake, but
`ServeAttached` performed that handshake directly in its accept loop. A client
that opened the per-user pipe and sent nothing blocked every legitimate
replacement connector for the full deadline. Pending accepted connections were
also not tracked, so broker cancellation could wait for their deadline instead
of closing them immediately.

Starting unbounded handshake goroutines would remove head-of-line blocking but
create a descriptor/goroutine exhaustion path. Installing registry generations
concurrently could also let an older completion replace the frame link after a
newer registry attachment.

## Decision

Split server attach into two wire-compatible phases:

1. up to 16 workers concurrently read and validate the bounded HELLO+ATTACH
   prefix without mutating the registry;
2. one installation lock serializes authentication, registry generation
   creation, summary/ack completion, callback, and `framed.Link.Attach`.

Pending handshakes are tracked and closed during shutdown. The connection is
removed from the pending set only after ownership transfers to the frame link.
The generic broker `Serve` path independently caps active connector handlers at
32. Connections beyond either cap are closed before a goroutine is created.

## Consequences

- An unauthenticated stalled client cannot delay a valid connector reattach.
- Cancellation closes pending handshakes immediately instead of waiting up to
  15 seconds.
- Registry generation order and active frame-link order remain identical.
- The attach wire format, token, summary, and resume acknowledgement are
  unchanged.
