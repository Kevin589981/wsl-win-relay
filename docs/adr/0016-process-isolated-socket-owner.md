# ADR-0016: Process-Isolated Windows Socket Owner

## Status

Accepted for implementation. The broker frontend/bridge-worker/socket-host
split is introduced behind the existing broker endpoint; the socket host owns
relay state while the two outer processes remain replaceable local bridges.

## Context

The persistent broker preserves connections across connector replacement, but
the process accepting the connector transport must be replaceable as well. If
that bridge process crashes while it owns the relay server, the operating
system closes its Windows TCP/UDP sockets before a replacement can attach.
Rebuilding mappings is useful for new traffic, but it cannot preserve an
established stream.

## Decision

Run three roles from the broker executable:

1. The **frontend** owns the public per-user IPC endpoint. It accepts connector
   byte streams and forwards each one to the worker over a private local IPC
   endpoint. It does not create relay state or network sockets.
2. The **bridge worker** owns only a private IPC endpoint. It forwards each
   frontend byte stream to the socket host and has no network socket or relay
   registry of its own.
3. The **socket host** owns the attach registry, relay server, upstream
   dialers, and all Windows network sockets. It uses the existing attach/resume
   protocol, so either outer bridge can be replaced without changing socket
   ownership.

The frontend derives a private bridge-worker endpoint from the configured
public endpoint and starts the bridge worker when one is not already
reachable. The bridge worker derives a second private socket-host endpoint
and starts or reuses the socket host. A new frontend first probes the bridge
worker; a replacement bridge worker first probes and reuses the socket host,
preserving its instance identity and socket state. Separate same-user control
endpoints let normal shutdown cascade from frontend to bridge worker to socket
host; an uncatchable crash has no opportunity to send those requests, so the
socket host remains alive.

The systemd broker unit uses `KillMode=process` so a restart of the frontend
does not terminate the bridge worker or socket host as part of cgroup cleanup.
A normal shutdown sends `STOP` over the private control endpoint before
exiting.

The frontend bridge is byte-transparent. Attach authentication and protocol
validation remain solely in the worker, avoiding a second implementation of
the wire contract and keeping stale connector teardown semantics unchanged.
Both outer roles continuously reap children they start; a replacement bridge
is started only after its owned child has been stopped or observed to exit.
Processes that merely reuse an already-running role are not claimed or
terminated by the new parent.

## Consequences

### Positive

- A frontend or bridge-worker crash no longer closes socket-host-owned
  established sockets.
- The existing WSL proxy and connector require no new protocol for frontend
  replacement; they reconnect to the same worker registry.
- Socket-host state remains bounded by the existing relay flow-control limits.
- Public IPC remains per-user local transport, with no new network listener.

### Negative

- Broker startup now involves two additional processes and three local IPC
  endpoints.
- A socket-host crash still loses sockets; this decision isolates both bridge
  failures, not arbitrary kernel or host failure.
- Worker lifecycle and orphan cleanup must be tested for graceful stop, stale
  endpoints, frontend restart, bridge-worker restart, and abnormal child exit;
  both supervisors reap children they started while preserving reuse of
  externally-owned roles.

## Rollout

1. Add socket-host role, bridge-worker role, and frontend byte bridges while preserving the existing
   command-line endpoint and token flags.
2. Add broker frontend- and bridge-worker-crash integration tests that verify
   in-flight streams plus automatic, explicit, and strict mappings.
3. Enable the split for broker-mode service startup and retain the remaining
   socket-host-crash boundary. The implementation includes cascading control
   endpoints and both crash-recovery paths.
