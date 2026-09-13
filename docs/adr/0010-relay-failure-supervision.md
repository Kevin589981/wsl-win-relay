# ADR-0010: Supervise Relay Sessions at the Process Boundary

## Status
Accepted for stdio mode; broker hot reconnect added by ADR-0015 and ADR-0016

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
again and recreates configured reverse mappings. The strict control socket stays
alive in the WSL process, so its live leases are rebound to the new relay
session instead of being discarded.

Treat local bind errors, invalid configuration, capability mismatches, and
reverse registration failures during a healthy session as fatal. The existing
systemd user unit may additionally restart the whole proxy after a fatal exit.
If the Windows child has already exited normally with a non-zero status, treat
that status as fatal as well; otherwise a malformed Windows-side configuration
would look like an EOF and trigger an infinite supervisor loop.

Strict listener leases that could not be rebound because the replacement
Windows endpoint was temporarily unavailable remain owned by the WSL control
daemon. A serialized background retry loop revisits only leases without an
active reservation, so a later port release can restore that listener without
closing or disrupting healthy mappings.

## Consequences

### Positive

- A direct invocation recovers from a crashed or prematurely exited Windows
  relay without requiring systemd.
- Local SOCKS5 and HTTP listener sockets can stay bound while a replacement
  relay session is starting; new dial/UDP operations wait for that session.
- Every new session starts with clean stream IDs, flow-control windows, UDP
  associations, and listener registrations.
- The strict control socket can outlive an individual relay child. Live leases
  retain their WSL process ownership and are rebound and recommitted on the
  replacement session; a temporary bind failure is logged and retried by the
  next session.
- Real configuration errors are not hidden by an unbounded retry loop.

### Negative

- Existing SOCKS connections and reverse-forward client connections are lost
  when the relay session fails. The reconnecting dialer only covers new local
  operations; lease rebinding does not migrate an in-flight protocol stream.
- New relay starts back off to thirty seconds at most, avoiding a restart storm
  while keeping recovery automatic.
- A stable session resets the backoff so a later isolated failure recovers
  promptly.
- Broker mode supplies the session-independent dialer, replayable mapping
  registry, and durable socket owner needed for hot reconnect. This stdio-mode
  limitation remains when the broker is not selected.
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
