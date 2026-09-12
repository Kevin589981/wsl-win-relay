# ADR-0016: Process-Isolated Windows Socket Owner

## Status

Accepted for implementation. The broker frontend/worker split is introduced
behind the existing broker endpoint; the worker owns relay state while the
frontend remains a replaceable local IPC bridge.

## Context

The persistent broker preserves connections across connector replacement, but
the broker executable still owns the Windows TCP/UDP sockets. If that process
crashes, the operating system closes its sockets before a replacement can
attach. Rebuilding mappings is useful for new traffic, but it cannot preserve
an established stream.

## Decision

Run two roles from the broker executable:

1. The **frontend** owns the public per-user IPC endpoint. It accepts connector
   byte streams and forwards each one to the worker over a private local IPC
   endpoint. It does not create relay state or network sockets.
2. The **worker** owns the attach registry, relay server, upstream dialers, and
   all Windows network sockets. It uses the existing attach/resume protocol,
   so a replacement frontend is just another connector transport from the
   worker's perspective.

The frontend derives a private worker endpoint from the configured public
endpoint and starts the worker when one is not already reachable. A new
frontend first probes that endpoint and reuses a live worker, preserving its
instance identity and socket state. A separate same-user control endpoint
lets a normally stopping frontend request worker shutdown; an uncatchable
frontend crash has no opportunity to send that request, so the worker remains
alive. Service-level stop still terminates the worker through the control
path and the systemd process group.

The frontend bridge is byte-transparent. Attach authentication and protocol
validation remain solely in the worker, avoiding a second implementation of
the wire contract and keeping stale connector teardown semantics unchanged.

## Consequences

### Positive

- A frontend crash no longer closes worker-owned established sockets.
- The existing WSL proxy and connector require no new protocol for frontend
  replacement; they reconnect to the same worker registry.
- Worker state remains bounded by the existing relay flow-control limits.
- Public IPC remains per-user local transport, with no new network listener.

### Negative

- Broker startup now involves an additional process and two local IPC
  endpoints.
- A worker crash still loses sockets; this decision isolates frontend failure,
  not arbitrary kernel or host failure.
- Worker lifecycle and orphan cleanup must be tested for graceful stop, stale
  endpoints, and frontend restart.

## Rollout

1. Add worker role and frontend byte bridge while preserving the existing
   command-line endpoint and token flags.
2. Add a broker frontend-crash integration test that verifies an in-flight
   stream plus automatic, explicit, and strict mappings.
3. Enable the split for broker-mode service startup and document the remaining
   worker-crash boundary. The implementation now includes the worker control
   endpoint and frontend-crash integration coverage.
