# ADR-0016: Process-Isolated Windows Socket Owner

## Status

Accepted for implementation. The broker frontend/bridge-worker/socket-host
bridge/socket-owner split is introduced behind the existing broker endpoint;
only the socket owner owns relay state and Windows network sockets.

## Context

The persistent broker preserves connections across connector replacement, but
every process in front of the relay server must also be replaceable. If a
connector bridge owns the relay server, the operating system closes its
Windows TCP/UDP sockets before a replacement can attach. Rebuilding mappings is
useful for new traffic, but it cannot preserve an established stream.

## Decision

Run four roles from the broker executable:

1. The **frontend** owns the public per-user IPC endpoint. It accepts connector
   byte streams and forwards each one to the worker over a private local IPC
   endpoint. It does not create relay state or network sockets.
2. The **bridge worker** owns only a private IPC endpoint. It forwards each
   frontend byte stream to the socket-host bridge and has no network socket or
   relay registry of its own.
3. The **socket-host bridge** owns only a private IPC endpoint. It forwards
   connector bytes to the socket owner and has no relay state or Windows network
   socket of its own.
4. The **socket owner** owns the attach registry, relay server, upstream
   dialers, and all Windows network sockets. It uses the existing attach/resume
   protocol, so any outer bridge can be replaced without changing socket
   ownership.

The frontend derives a private bridge-worker endpoint from the configured
public endpoint and starts the bridge worker when one is not already
reachable. The bridge worker derives separate socket-host-bridge and
socket-owner endpoints, starting or reusing each role. A replacement of any
outer bridge probes and reuses the same socket owner, preserving its instance
identity and socket state. Separate same-user control endpoints let normal
shutdown cascade from frontend to bridge worker to socket-host bridge to socket
owner; an uncatchable outer crash has no opportunity to send those requests,
so the socket owner remains alive.

The systemd broker unit uses `KillMode=process` so a restart of the frontend
does not terminate the bridge worker, socket-host bridge, or socket owner as
part of cgroup cleanup.
A normal shutdown sends `STOP` over the private control endpoint before
exiting.

The frontend bridge is byte-transparent. Attach authentication and protocol
validation remain solely in the worker, avoiding a second implementation of
the wire contract and keeping stale connector teardown semantics unchanged.
Both outer roles continuously reap children they start; a replacement bridge
is started only after its owned child has been stopped or observed to exit.
Processes that merely reuse an already-running role are not claimed or
terminated by the new parent. Bridge workers probe the socket-host bridge, and
the socket-host bridge probes the socket-owner control endpoint periodically; a
failed probe terminates the failed outer role so the frontend supervisor can
rebuild it while the owner and registrations remain alive. Role probes include
the attach token and role identity and are validated with a constant-time
comparison, so an endpoint left behind by a different broker identity or
binary role is not mistaken for a reusable process.
When a token mismatch is explicit, the supervisor sends the private `STOP`
request and waits for the old control endpoint to disappear before starting a
replacement; ordinary connection failures never terminate an externally-owned
role.

## Consequences

### Positive

- A frontend, bridge-worker, or socket-host bridge crash no longer closes
  socket-owner-owned established sockets.
- The existing WSL proxy and connector require no new protocol for frontend
  replacement; they reconnect to the same worker registry.
- Socket-owner state remains bounded by the existing relay flow-control limits.
- Public IPC remains per-user local transport, with no new network listener.

### Negative

- Broker startup now involves three additional processes and four local IPC
  endpoints.
- A socket-owner crash still loses sockets; this decision isolates connector
  bridge failures, not arbitrary kernel or host failure. Health probing rebuilds
  new traffic and mappings, but cannot preserve streams owned by the crashed
  socket owner.
- Worker lifecycle and orphan cleanup must be tested for graceful stop, stale
  endpoints, frontend restart, bridge-worker restart, and abnormal child exit;
  both supervisors reap children they started while preserving reuse of
  externally-owned roles.

## Rollout

1. Add socket-owner, socket-host bridge, bridge-worker, and frontend byte
   bridges while preserving the existing command-line endpoint and token flags.
2. Add crash integration tests for every outer bridge that verify in-flight
   streams plus automatic, explicit, and strict mappings.
3. Enable the split for broker-mode service startup and retain the remaining
   socket-owner-crash boundary. The implementation includes cascading control
   endpoints and independent health probes.
