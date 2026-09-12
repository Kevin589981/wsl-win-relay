# ADR-0013: Add Allowlisted UDP Discovery as a Separate Adapter

## Status

Accepted

## Context

TCP listener discovery can use the `0A` state in `/proc/net/tcp{,6}`. Linux
procfs does not expose an equivalent server state for UDP: ordinary UDP
clients and services commonly appear with the same `07` state, and both may
bind an unconnected local port. Mapping every UDP row would therefore expose
ephemeral client sockets and create surprising Windows listeners.

The relay already has exact UDP reverse forwarding and strict UDP `bind()`
coordination. Some users still need a zero-configuration-ish fallback for a
small, known set of UDP services when changing their launch command is not
practical.

## Decision

Add a separate, explicitly enabled UDP discovery adapter. It scans
`/proc/net/udp` and `/proc/net/udp6`, accepts only non-zero local ports whose
remote endpoint is unconnected, and requires a non-empty UDP port allowlist.
The adapter reuses the existing reverse-datagram lifecycle, IPv4/IPv6 host
selection, rejection retry logging, session cleanup, and exclusion policy.

The TCP allowlist is not implicitly reused: users must opt in with
`-auto-forward-udp` and provide `-auto-forward-udp-include`, or set
`auto_forward.udp_enabled` and `auto_forward.udp_include` in configuration.
Strict UDP `bind()` interposition remains the only mode that can coordinate a
specific application bind atomically and return a Windows bind error to that
application.

## Consequences

### Positive

- Common fixed-port UDP services can be mirrored without a manual
  `-reverse-udp` entry.
- The mandatory allowlist limits accidental exposure from procfs ambiguity.
- UDP discovery shares the tested relay/session lifecycle instead of adding a
  second transport or protocol.

### Negative

- A client can still be mapped if it binds an allowlisted port; procfs cannot
  distinguish that case.
- Detection has polling latency and Windows rejection is reported in logs,
  not as a retroactive failure of an already successful `bind()`.
- Users must maintain a separate UDP allowlist.

## Alternatives Considered

**Map every `/proc/net/udp` row:** rejected because ephemeral and service
sockets are indistinguishable and would cause unsafe port exposure.

**Infer server sockets from process names or traffic:** rejected because it is
unstable, racy, and cannot provide a portable correctness contract.

**Require strict interposition for every UDP service:** retained as the exact
mode, but it requires launching the application through the native wrapper.
