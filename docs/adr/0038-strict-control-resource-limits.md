# ADR-0038: Bound Strict-Listener Control Resources

## Status

Accepted

## Context

The strict-listener Unix socket limits each request line and applies a read
deadline, but its accept loop, lease registry, and per-lease process-owner map
previously had no aggregate bounds. A burst could create many request
goroutines within the deadline, and a process tree could retain owner metadata
or committed Windows reservations indefinitely until process reaping.

## Decision

Apply three independently configurable limits:

- `strict_max_connections` defaults to 64 active control requests;
- `strict_max_leases` defaults to 512 active plus pending reservations;
- `strict_max_owners_per_lease` defaults to 256 process owners.

Each accepts `1..65535` through JSON or its corresponding CLI flag. Excess
control connections are closed before a handler starts. RESERVE atomically
claims a pending lease slot before calling the Windows backend, so concurrent
requests cannot overshoot the combined active/pending limit. The slot is
released on every failure or converted to an active lease on success.

ADOPT rejects only a new owner beyond the cap; an existing owner may repeat an
idempotent update. Protocol errors use `ENOSPC` (`28`) and do not alter existing
leases or owners.

## Consequences

- Strict coordination has fixed handler, reservation, and ownership metadata
  bounds even during relay outages or fork bursts.
- Existing leases remain usable when a limit is saturated.
- Operators with unusually large process trees can raise explicit limits
  without recompiling the native interposer or supervisor.
