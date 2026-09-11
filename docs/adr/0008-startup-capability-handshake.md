# ADR-0008: Require a Startup Capability Handshake

## Status
Accepted

## Context

The WSL proxy can accidentally launch an old relay executable or an unrelated program. Per-frame magic and version checks detect malformed traffic only after a request is made, allowing local proxy listeners to appear healthy prematurely.

## Decision

Exchange HELLO/HELLO_OK frames on control stream zero immediately after the Windows child starts. Advertise a capability bitset for TCP, reverse forwarding, reserve/commit, UDP, and flow control. The WSL entrypoint requires all capabilities used by the current release and applies a five-second timeout before starting control sockets or registering mappings.

Real-process integration tests are opt-in through `WSL_WIN_RELAY_E2E=1` and require a freshly built Windows relay. Ordinary unit tests never trust stale ignored binaries.

## Consequences

- Configuration errors and incompatible binaries fail during startup.
- Future releases can negotiate optional features without inferring them from version strings.
- The entrypoint currently requires the full current capability set; compatibility policy can relax this when optional feature profiles are introduced.
