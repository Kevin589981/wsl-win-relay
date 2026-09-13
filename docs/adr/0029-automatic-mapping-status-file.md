# ADR-0029: Publish Automatic Mapping Status Locally

## Status
Accepted

## Context

Automatic mappings are discovered after WSL listeners start. With a non-zero
Windows port offset, logs are not a stable integration surface for local
operators or tooling that needs the actual Windows-facing address. A network
status endpoint would add another exposed listener and authentication problem
to an otherwise local control plane.

## Decision

Add an optional `auto_forward.status_file` configuration field and
`-auto-forward-status` flag. The watcher writes a versioned JSON document with
the publishing process ID, an RFC3339 update timestamp, active network family,
Windows address, and WSL address. Updates use a
same-directory temporary file, mode `0600`, `fsync`, and rename so readers
never observe a partial document. The watcher serializes status writes and
removes the file during shutdown. A failed status write is logged but does not
stop forwarding.

## Consequences

- Local tools can discover offset mappings without parsing logs or opening a
  new network API.
- The default remains disabled and the relay wire protocol is unchanged.
- A crash can leave an old file until an operator removes it; consumers must
  treat the file as advisory and use the process ID and timestamp when making
  safety-critical decisions.
