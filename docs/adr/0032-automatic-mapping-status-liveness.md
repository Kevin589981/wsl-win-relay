# ADR-0032: Make Automatic Mapping Status Liveness Verifiable

## Status

Accepted

## Context

The optional automatic-mapping status file exposes Windows-allocated ports to
local consumers. Atomic replacement prevents partial reads, but a process crash
can leave a syntactically valid snapshot behind. The original file changed only
when mappings changed, so `updated_at` could not reliably distinguish a stable
live mapping from a crashed publisher.

## Decision

Keep the local version-1 JSON file and add a 30-second heartbeat. An unchanged
mapping set is still not rewritten within that interval. Export a shared,
bounded reader that rejects symlinks, non-regular files, files larger than 1
MiB, unknown JSON fields, trailing values, unsupported versions, invalid
timestamps, malformed addresses, and inconsistent mapping state.

The supported freshness window defaults to two minutes. Consumers must require
both a fresh timestamp and a live `process_id`; neither signal alone is enough
because clocks can move and process identifiers can eventually be reused.

## Consequences

- A crash leaves a file temporarily, but supported consumers reject it after a
  bounded interval and can reject it immediately when the publisher PID exits.
- Stable mappings cause at most one small atomic status write every 30 seconds.
- The on-disk schema and version remain compatible with existing readers.
- No additional network or control-socket endpoint is introduced.
