# ADR-0039: Bound Broker Role Connections

## Status

Accepted

## Context

The replaceable broker frontend, bridge worker, and socket-host bridge each
accepted internal data connections into an unbounded goroutine set. Every
broker role also exposed a private control endpoint whose request line was
size-limited but had no read deadline or aggregate handler limit. A same-user
process could therefore retain descriptors and goroutines by opening idle
connections, including during role shutdown.

## Decision

Use the shared bounded connection server for every replaceable-role data loop
and every role control loop:

- each data bridge layer permits at most 32 active connections;
- each private control endpoint permits at most 16 active requests;
- a control connection has a two-second deadline covering its one request and
  response;
- cancellation closes the listener and all accepted connections, then waits
  for their handlers to exit.

Connections accepted while a limit is saturated are closed before a handler is
started. The limits are fixed because these endpoints form a private,
single-user implementation topology rather than operator-facing relay
capacity. Public SOCKS/HTTP and relay object limits remain independently
configurable where appropriate.

## Consequences

- Idle or bursty same-user clients cannot create unbounded role goroutines or
  descriptors.
- Saturation rejects only new internal connections and leaves established
  bridges intact.
- Normal role shutdown no longer depends on individual bridge peers closing
  first.
- Very large deployments cannot raise the internal per-layer limit without a
  new design decision; 32 is intentionally above the expected connector count.
