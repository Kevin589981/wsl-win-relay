# ADR-0007: Use Credit-Based Per-Stream Flow Control

## Status
Accepted

## Context

All connections share one ordered process pipe. Writing a DATA frame directly to a slow destination from the protocol reader causes head-of-line blocking: unrelated SOCKS, HTTP, reverse, and UDP flows stop making progress. Unbounded per-stream queues merely exchange blocking for memory exhaustion.

## Decision

Limit stream DATA frames to 32 KiB and give each direction of each TCP stream an implicit 256 KiB initial credit window. A sender consumes credit before writing DATA. A receiver restores credit with `WINDOW_UPDATE` only after bytes have been delivered to the consuming socket or application.

Windows socket writes run in per-stream workers rather than the global frame reader. Queues are bounded to the advertised window. Invalid credit growth or queue overflow resets only the offending stream.

UDP remains message-oriented and bounded by the overall frame size; it will receive a separate drop/backpressure policy if sustained datagram workloads require it.

## Consequences

### Positive

- A stalled connection cannot block control frames or other streams.
- Memory usage is bounded per TCP direction.
- Backpressure follows actual downstream consumption.

### Negative

- Every 256 KiB transferred requires window-update traffic.
- Protocol implementations must preserve credit accounting exactly.
- Window sizing may require future tuning for high-bandwidth workloads.
