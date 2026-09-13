# ADR-0034: Bound Relay Registries and Open Work

## Status

Accepted

## Context

Frame sizes and per-stream credit windows bound individual allocations, but
the client and server registries previously accepted unlimited stream,
listener, and datagram objects. The Windows server also started a goroutine for
every asynchronous OPEN frame before checking a registry. A malfunctioning
loopback client or authenticated peer could therefore exhaust sockets, memory,
or goroutines without violating any per-frame limit.

## Decision

Apply independent default caps on both WSL and Windows:

- 512 concurrent TCP streams;
- 256 reverse TCP listeners;
- 64 outbound UDP associations;
- 64 reverse UDP listeners;
- 256 source flows per reverse UDP listener;
- 32 concurrent asynchronous open operations;
- 16 queued packets per UDP object.

Local client API calls fail with `ErrResourceLimit`. Peer requests receive the
matching OPEN/LISTEN/DATAGRAM error frame. A Windows connection accepted after
the TCP stream cap is reached is closed immediately without disturbing existing
streams. UDP queues retain their loss-oriented non-blocking behavior.

The open-operation gate is acquired before a goroutine is created. TCP listener
registration releases its open slot after bind and acknowledgement, while its
long-running accept loop remains accounted for by the listener registry.

## Consequences

- Registry size and worst-case buffered payload memory now have global bounds.
- Broker detach does not add a second frame queue: writers wait for attachment
  and remain governed by object contexts and these registry caps.
- Saturation affects only new work; existing streams and mappings continue.
- The wire format is unchanged and older peers receive ordinary error frames.
