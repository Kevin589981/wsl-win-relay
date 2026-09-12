# ADR-0015: Persistent Windows Ownership and Session Attach

## Status

In progress: attach registry, resume wire contract, local IPC abstraction, and
transport-independent broker session core are implemented; socket migration is
still pending.

## Context

The current relay child owns every Windows TCP/UDP socket and the multiplexed
protocol state. A broken stdio pipe therefore destroys both the transport and
the sockets. The WSL supervisor can recreate mappings and new requests, but it
cannot preserve an already established stream.

Retrying the child process cannot solve this: a new child has no file handles,
stream identifiers, flow-control credits, or upstream proxy connections from
the old child. Reusing the current `Client` after EOF would be unsafe because
the peer state belongs to the terminated process.

## Decision

Add a Windows-side broker mode in a later implementation stage. The broker
owns outbound sockets, reverse listeners, UDP flow tables, and stream state for
the lifetime of the broker process. A small Windows connector, launched by WSL
through interop, bridges the WSL byte stream to the broker over a Windows-only
IPC transport. The WSL proxy may restart the connector without restarting the
broker.

The attach protocol has four properties:

1. **Explicit broker identity:** the broker issues a random per-instance token
   and a monotonically increasing session epoch. The connector proves the
   token on every attach; a stale or unknown token is rejected.
2. **Replayable ownership:** each stream/listener/UDP association has a stable
   registry identifier independent of one connector connection. Attach starts
   with a capability and registry summary exchange, then resumes only entries
   that both sides acknowledge.
3. **Bounded buffering:** data queued while no connector is attached is bounded
   per stream and globally. Once limits are reached, the broker resets the
   affected stream rather than growing memory without limit. UDP keeps its
   existing loss-oriented policy.
4. **Generation-safe teardown:** a connector may close only state belonging to
   its attach epoch. A delayed close from an older connector cannot tear down a
   newer attachment. Explicit destructive cleanup remains single-shot.

The first transport implementation should use a per-user Windows IPC endpoint
(named pipe or an equivalent Windows-only local transport), not a TCP listener
that would expand the attack surface. The existing stdio path remains the
default until the broker and connector have interoperable tests and a real WSL
failure-mode check.

## Attach sequence

```text
WSL proxy       Windows connector        Windows broker
    |                    |                      |
    |-- HELLO ---------->|                      |
    |                    |-- ATTACH(token) ---->|
    |                    |<-- ATTACH_OK(epoch)--|
    |                    |<-- REGISTRY_SUMMARY -|
    |<-- RESUME_SUMMARY -|                      |
    |-- RESUME_ACK ------>|                      |
    |<======= framed stream data ==============>|
```

The exact frame names are implementation details, but the ordering is a
contract: no resumed data is delivered before both sides have acknowledged the
same epoch and registry entries. A failed attach leaves broker-owned sockets
alive and makes the connector retry with bounded backoff.

## Consequences

### Positive

- Existing TCP streams and reverse listeners can survive a connector/stdio
  failure because their socket ownership is outside the connector.
- The current session supervisor and process-scoped automatic mapping logic can
  remain as the fallback path.
- IPC can be restricted to the same Windows user instead of exposing a network
  port.

### Negative

- A broker is an additional long-running process with installation, shutdown,
  and crash-recovery responsibilities.
- Resume requires protocol changes, registry reconciliation, and careful flow
  control; it cannot be implemented as a transparent retry of `Read`/`Write`.
- If the broker itself exits, its sockets and upstream connections are still
  lost. The broker must therefore have its own bounded restart policy, and the
  WSL side must report that distinction.

## Rollout order

1. Extract a transport/session interface and add generation-safe attach tests
   using in-memory endpoints.
2. Add a Windows-only broker and connector with per-user IPC, initially
   supporting health and capability exchange only.
3. Move outbound stream ownership into the broker and add stable registry IDs,
   bounded detached buffering, and resume acknowledgements.
4. Migrate reverse TCP/UDP listeners and automatic mapping registry entries.
5. Make broker mode opt-in, run real HNS-failure and connector-restart tests,
   then consider making it the long-running-service default.
