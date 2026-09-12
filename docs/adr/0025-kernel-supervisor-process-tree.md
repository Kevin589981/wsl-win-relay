# ADR-0025: Process-Tree Model for Kernel Supervisor Phase Two

## Status

Accepted, implementation staged

## Context

The phase-one ptrace supervisor coordinates direct static amd64 listeners, but
it deliberately rejects process creation. A full static-binary adapter must
preserve the same Windows-before-WSL contract when a service forks workers,
creates process-style `clone()` children, or creates ordinary threads. The
current implementation has one tracee, one pending syscall record, and an fd
table keyed only by descriptor number, so extending it by allowing fork/clone
would make inherited descriptors ambiguous and could cause a child to outlive
the lease owner known to the control server.

## Decision

Phase two will introduce an explicit supervisor process table:

- Every traced task has its own pid/tid, syscall-entry state, pending record,
  and fd table keyed by `(task id, fd)`.
- `PTRACE_O_TRACEFORK`, `PTRACE_O_TRACEVFORK`, and `PTRACE_O_TRACECLONE` will
  attach children before they can execute another syscall. The event handler
  will clone inherited fd state and issue `ADOPT child-pid lease` once per
  inherited lease.
- Lease teardown will use owner-scoped `RELEASE pid lease`; a lease is closed
  by the control server only after its final owner disappears. `CLOSE` remains
  reserved for a lease with no child owner.
- `CLONE_THREAD` tasks share the process lease owner but keep separate fd and
  pending-syscall state. Thread-group exit must release only descriptors owned
  by the exiting task and leave sibling state intact.
- `vfork()` remains fail-closed until the shared-address-space and exec/
  `_exit` lifecycle is modeled; it must not be treated as an ordinary fork.
- The adapter remains opt-in. Unsupported architectures and unrecognized
  ptrace events fail closed with a diagnostic rather than silently falling back
  to polling.

The control protocol is unchanged; this is an ownership and supervision change
inside the kernel adapter. The default dynamic interposer remains the preferred
path for dynamically linked applications because it already implements the
lower-overhead process lifecycle hooks.

## Consequences

### Positive

- Static services can eventually fork workers without losing Windows lease
  ownership or descriptor cleanup.
- Per-task state makes duplicate descriptors and thread-group teardown
  testable instead of dependent on a global fd namespace.
- Existing `ADOPT`/`RELEASE` protocol semantics are reused.

### Negative

- The supervisor becomes a multi-task state machine and needs event-ordering,
  exit, and signal regression tests.
- Ptrace overhead and platform restrictions remain for static targets.

### Neutral

- Phase one continues to reject process creation until phase two lands; this is
  an explicit compatibility boundary, not an automatic fallback.

## Alternatives Considered

**Allow fork/clone without tracing children**

Rejected: the child could create a listener after the parent lease is reaped,
violating the Windows-before-WSL contract.

**Use one global fd table and reference counts**

Rejected: fd numbers are reused independently in different tasks, and a close
in one task could release another task's descriptor.

**Replace ptrace with a global seccomp user-notification daemon**

Deferred: it can reduce per-syscall stops but requires a privileged listener,
more deployment machinery, and equivalent process-tree bookkeeping.

