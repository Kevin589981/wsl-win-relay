# ADR-0021: Retry Connector Attach During Frontend Replacement

## Status

Accepted

## Context

The host-level broker supervisor replaces the public frontend after an
abnormal exit. During that replacement the local IPC endpoint can briefly be
absent, or an accepted connection can close before the attach handshake
finishes. A one-shot connector that exits on the first transport error makes
the WSL proxy observe a permanent relay failure even though the broker is
already recovering.

## Decision

The connector exposes a bounded retrying attach path. It retries local-IPC
dial and handshake transport errors with exponential backoff starting at 100
milliseconds and capped at two seconds, for at most 30 seconds. A malformed
connector configuration or an attach rejection (including a bad token) is
terminal and is not retried. Cancellation still stops the attempt immediately.

The command-line connector uses this path by default; the lower-level single
attempt `Connect` API remains available for callers that need explicit failure
semantics.

## Consequences

- A WSL proxy process survives the short endpoint gap caused by a supervised
  frontend restart.
- Bad credentials and invalid configuration remain visible instead of being
  hidden behind an endless retry loop.
- A permanently unavailable endpoint still fails after a finite 30-second
  window, preserving service-manager restart behavior.
