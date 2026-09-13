# ADR-0008: Require a Startup Capability Handshake

## Status
Accepted

## Context

The WSL proxy can accidentally launch an old relay executable or an unrelated program. Per-frame magic and version checks detect malformed traffic only after a request is made, allowing local proxy listeners to appear healthy prematurely.

## Decision

Exchange HELLO/HELLO_OK frames on control stream zero immediately after the Windows child starts. Advertise a capability bitset for TCP, reverse TCP, reverse UDP, reserve/commit, UDP, flow control, and optional bound-listener address reporting. The WSL entrypoint requires the core capabilities and adds optional capabilities only when their corresponding mode is configured. It applies a configurable five-second timeout before registering relay mappings. The strict control socket may start before the handshake so WSL listener reservations can wait for a recovering session; live leases are rebound after the replacement handshake succeeds.

Real-process integration tests are opt-in through `WSL_WIN_RELAY_E2E=1` and require a freshly built Windows relay. Ordinary unit tests never trust stale ignored binaries.

## Consequences

- Configuration errors and incompatible binaries fail during startup.
- Future releases can negotiate optional features without inferring them from version strings.
- The entrypoint requires the core capability set; optional feature profiles
  add their negotiated bits without breaking older peers in the default mode.
