# ADR-0024: Add an Opt-In Static-Binary Kernel Supervisor

## Status

Superseded by ADR-0025

## Context

The native `LD_PRELOAD` interposer cannot affect static ELF or setuid/setgid
programs. The strict launcher now refuses those targets, but the requested
Windows-before-WSL bind contract remains unavailable for applications that
cannot be rebuilt dynamically.

## Decision

Add `native/strict_supervisor.c`, an opt-in Linux amd64 ptrace supervisor
selected with `wsl-win-relay-run --kernel`. It launches the target under
`PTRACE_SYSCALL`, observes direct `socket`, `bind`, `listen`, `close`, and
descriptor-duplication syscalls, and uses the existing control socket to
reserve/commit/abort Windows mappings. A reservation failure is injected as
the target syscall's errno before the syscall executes. TCP reservations are
committed only after Linux `listen()` succeeds; UDP reservations are rolled
back if Linux `bind()` fails.

The phase-one release intentionally supported a directly executed single-
process amd64 target and rejected process creation. Process-tree ownership is
now being extended under ADR-0025; the original interposer remains the default
because it has broader low-overhead lifecycle coverage.

## Consequences

- Static TCP and UDP services can opt into strict Windows bind coordination.
- The existing control protocol and Windows relay implementation are reused;
  no second mapping protocol is introduced.
- Ptrace adds a process supervisor and syscall overhead, so it is explicit and
  should be expanded only with dedicated lifecycle tests.
- The remaining kernel-level matrix item now narrows to process ownership and
  architecture coverage rather than having no static implementation at all.
