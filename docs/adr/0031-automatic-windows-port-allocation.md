# ADR-0031: Allow Windows to Allocate Automatic Mapping Ports

## Status
Accepted

## Context

A fixed Windows port offset is deterministic but cannot guarantee that the
translated port is free. Probing candidates from WSL is inherently racy because
another Windows process can bind between the probe and relay registration.
ADR-0030 established a negotiated way for Windows to return the actual address
selected by a port-zero bind.

## Decision

Add `auto_forward.windows_port_auto` and `-auto-forward-port-auto`. When enabled,
TCP and allowlisted UDP watchers request a Windows listener on port zero and
require `CapabilityListenBoundAddress`. The returned non-zero address becomes
part of the active mapping state and is used for logs and status publication.
Missing or malformed bound-address responses reject and close the mapping.

Automatic allocation and `windows_port_offset` are mutually exclusive. Both
remain opt-in; the default continues to mirror the WSL port on Windows. A relay
session reset closes the old allocation and obtains a new one, which is then
reflected in the status file.

## Consequences

- Windows chooses a free port atomically, avoiding both mirrored same-port
  conflicts and fixed-offset collisions.
- The assigned port may change after relay-owner replacement, so consumers
  must observe status updates rather than cache it indefinitely.
- Older peers continue to work in default and fixed-offset modes. Auto mode
  fails its capability handshake instead of silently accepting an unknown port.
