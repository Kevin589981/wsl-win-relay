# ADR-0014: Process-Scoped Automatic Forwarding

## Context

Automatic listener discovery used to start inside each relay session. A child
restart therefore stopped the watcher and left its Windows mappings tied to a
dead client until the next session had fully rebuilt its own watcher.

## Decision

Run TCP and allowlisted UDP discovery for the lifetime of the WSL proxy process.
The watcher opens mappings through `sessionDialer`, which waits for a healthy
relay child and commits TCP reservations before returning their closer. When a
session changes, the proxy resets the watcher state: old mappings are closed,
rejection state is cleared, and the next scan recreates mappings through the
replacement session.

## Consequences

- Automatic mappings survive relay child replacement without requiring a new
  proxy process.
- A short restart window has no Windows listener for the WSL port; this is
  explicit and preferable to retaining a stale listener whose forwarding path
  is broken.
- Existing connections still belong to the failed session and are not moved;
  only listener mappings and new proxy operations are recovered.
- UDP automatic discovery keeps its separate allowlist and procfs ambiguity
  boundary documented in ADR-0013.
