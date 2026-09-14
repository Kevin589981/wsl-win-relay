# ADR-0019: Keep a Host Supervisor Above the Broker Frontend

## Status

Accepted

## Context

The broker already isolates the socket owner from connector and bridge
processes, but the public frontend is still the top-level executable. If that
frontend exits unexpectedly, a WSL service manager may not observe the failure
quickly enough to rebuild the local IPC endpoint. The socket owner can also be
left reusable while the public endpoint is unavailable.

## Decision

Add a `-supervise` mode to `wsl-win-broker`. The supervisor owns only a private
per-broker election endpoint; it launches the normal frontend as a child, forwards its standard streams, and
restarts it after abnormal exit with a bounded exponential delay (two seconds
through thirty seconds). A stable child resets the delay. Exit status zero is
treated as an intentional stop, and status two (argument/configuration error)
is fatal rather than restartable. The broker user-service wrapper starts this
mode by default.

The supervisor does not duplicate role health logic or own the attach token
beyond passing it to the child. The existing frontend/worker/socket-owner
roles remain responsible for endpoint reuse, token checks, and mapping
reconstruction. This keeps the host boundary small and avoids a second broker
implementation.

The election endpoint serializes supervisors across WSL restarts. A later
supervisor waits instead of racing the same frontend endpoint. If the previous
supervisor is gone but its Windows frontend remains, the new owner verifies the
frontend through a token-authenticated control endpoint and reuses it until it
exits; it then starts the replacement. Legacy frontends without the control
endpoint are treated conservatively as reachable and are never raced during an
upgrade.

## Consequences

- A crashed broker frontend is recreated without requiring a WSL service restart.
- Existing socket-owner state can be reused when the child frontend returns.
- A WSL restart cannot create competing broker frontend trees for one endpoint.
- Configuration errors remain visible and do not create a restart storm.
- The supervisor itself still depends on the host process manager or user
  session; a full WSL VM shutdown requires an external Windows service/task
  boundary, which remains a separate deployment concern.
