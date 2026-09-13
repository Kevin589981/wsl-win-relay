# ADR-0036: Keep Frame Dispatch Bounded and Non-Blocking

## Status

Accepted

## Context

One relay reader dispatches frames for every multiplexed object. Reverse UDP
data handling previously decoded an endpoint, performed DNS, and created a
per-source socket synchronously in that reader. A slow resolver therefore
blocked unrelated TCP streams, control frames, and reconnect progress.

TCP reverse-listener readiness also wrote every repeated `LISTEN_OK` or
`LISTEN_ERROR` into a one-element channel. A malformed peer could block the
reader with duplicate terminal responses. An inbound stream referencing a
missing listener was ignored, leaving the peer-side accepted socket alive.

## Decision

Each reverse UDP listener owns a 16-entry packet queue and one worker. Frame
dispatch performs only a non-blocking copy/enqueue; queue saturation drops the
new UDP packet. The worker performs decode, context-aware DNS, source-flow
lookup, and local UDP I/O. Listener close cancels an in-flight lookup and stops
the worker.

TCP listener readiness is published through a dedicated `sync.Once`, so the
first response wins and duplicates are ignored. `INBOUND_OPEN` for an unknown
listener receives an immediate `RESET` for its stream ID.

## Consequences

- Slow UDP endpoint work cannot head-of-line block multiplexed TCP or control
  traffic.
- Each reverse UDP listener adds at most one worker and a bounded packet queue.
- UDP retains its intentional loss behavior under pressure.
- Duplicate readiness and stale inbound frames cannot retain either reader
  goroutines or peer-side sockets.
