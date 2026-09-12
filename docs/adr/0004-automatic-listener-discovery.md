# ADR-0004: Add Polling Discovery Before Strict Listen Interposition

## Status
Accepted

## Context

Users want WSL TCP listeners to appear automatically on Windows even when HNS forwarding is broken. Linux exposes current listening sockets in `/proc/net/tcp` and `/proc/net/tcp6`, which can be inspected without changing applications. This observation happens after `listen(2)` succeeds, so it cannot force the original call to fail if Windows cannot bind the same port.

## Decision

Add an opt-in watcher that polls both proc files, keeps one representative
listener per address family and port, registers new Windows reverse forwards,
and removes mappings when WSL listeners disappear. A wildcard listener is the
representative when several sockets share a family and port. Keep IPv4 and
IPv6 mappings independent because Windows can bind both loopback families to
the same port. Bind Windows loopback by default. Support allowlists,
exclusions, and a configurable interval.

Treat strict synchronized rejection as a different mode implemented with pre-listen application interposition or an equivalent kernel-aware mechanism. Do not misrepresent polling as atomic cross-kernel binding.

## Consequences

### Positive

- Existing applications gain automatic Windows reachability without modification.
- Dynamic listener addition and removal require no daemon restart.
- Loopback binding and allowlists provide conservative defaults.

### Negative

- Detection has bounded polling latency.
- Windows bind rejection is logged and retried, but the WSL application is already listening.
- `/proc/net/tcp{,6}` is Linux-specific and only exposes TCP.
