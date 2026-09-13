# ADR-0040: Align Broker Registry and Wire Limits

## Status

Accepted

## Context

The attach protocol accepts at most 4096 stable entries in a registry summary,
but the transport-independent broker previously allowed its live entry map to
grow without a matching bound. Once it contained 4097 entries, the active
session could continue running while every future attach failed to encode its
summary. The broker would then be unable to recover from a connector outage.

## Decision

Export the attach protocol's `MaxSummaryEntries` constant and use it as the
broker's `MaxRegistryEntries` limit. `Register` returns the distinguishable
`ErrRegistryFull` before allocating a new ID when all 4096 slots are occupied.
Existing entries, their states, and the current connector remain unchanged.
Removing an entry immediately makes one slot available again.

The saturated registry is tested through the actual summary encoder so the
registration and wire limits cannot drift independently.

## Consequences

- Every broker state that can be created through `Register` remains encodable
  in a later attach/resume handshake.
- Saturation affects only new stable objects and cannot strand existing ones.
- Callers can classify capacity exhaustion with `errors.Is` and decide whether
  to retry after another object is removed.
- Raising the registry capacity requires a compatible attach-wire decision
  rather than an isolated broker setting.
