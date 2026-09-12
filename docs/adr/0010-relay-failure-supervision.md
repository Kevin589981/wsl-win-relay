# ADR-0010: Supervise Relay Sessions at the Process Boundary

## Status
Accepted

## Context

The Windows relay is a child process connected through a framed stdio
transport. If that process exits, every in-flight stream, datagram association,
and Windows-side reverse listener has lost its transport state. Reusing the
same client object after an EOF would risk carrying stale stream identifiers,
credits, and listener registrations into a new process.

The proxy should remain useful when the child crashes, but configuration and
local listener errors must still be visible instead of being retried forever.

## Decision

Treat a relay EOF or broken stdio transport during startup handshake or normal
operation as a transient session failure. The WSL entrypoint tears down the
failed session, starts a fresh Windows relay process after a bounded
exponential backoff (two seconds initially, capped at thirty seconds). After a
session has stayed healthy for at least one minute, a later failure starts again
at the initial two-second delay. Each restart performs the capability handshake
again and recreates configured reverse mappings and control state.

Treat local bind errors, invalid configuration, capability mismatches, and
reverse registration failures during a healthy session as fatal. The existing
systemd user unit may additionally restart the whole proxy after a fatal exit.
If the Windows child has already exited normally with a non-zero status, treat
that status as fatal as well; otherwise a malformed Windows-side configuration
would look like an EOF and trigger an infinite supervisor loop.

## Consequences

### Positive

- A direct invocation recovers from a crashed or prematurely exited Windows
  relay without requiring systemd.
- Local SOCKS5 and HTTP listener sockets can stay bound while a replacement
  relay session is starting; new dial/UDP operations wait for that session.
- Every new session starts with clean stream IDs, flow-control windows, UDP
  associations, and listener registrations.
- Real configuration errors are not hidden by an unbounded retry loop.

### Negative

- Existing SOCKS connections and reverse-forward client connections are lost
  when the relay session fails. The reconnecting dialer only covers new local
  operations; it does not migrate an in-flight protocol stream.
- New relay starts back off to thirty seconds at most, avoiding a restart storm
  while keeping recovery automatic.
- A stable session resets the backoff so a later isolated failure recovers
  promptly.
- True in-process hot reconnect would require a session-independent dialer,
  replayable mapping registry, and explicit handling for in-flight requests;
  it remains a future enhancement.
- A child terminated by an external signal is still considered a transient
  transport failure; only a normal non-zero exit is classified as fatal.

## Alternatives Considered

**Reuse the client after EOF:** rejected because protocol state and stream IDs
belong to the terminated transport.

**Retry every error:** rejected because port conflicts and invalid configuration
would produce an opaque restart loop.

**Require systemd for recovery:** rejected because direct command-line use is a
supported operating mode and should have a bounded recovery behavior of its
own.
