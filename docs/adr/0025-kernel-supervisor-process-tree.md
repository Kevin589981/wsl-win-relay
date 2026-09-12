# ADR-0025: Process-Tree Model for Kernel Supervisor Phase Two

## Status

Accepted, process/thread/vfork support implemented; unusual teardown follow-up

## Context

The phase-one ptrace supervisor coordinated direct static amd64 listeners, but
it deliberately rejected process creation. A full static-binary adapter must
preserve the same Windows-before-WSL contract when a service forks workers,
creates process-style `clone()` children, or creates ordinary threads. The
current implementation has one tracee, one pending syscall record, and an fd
table keyed only by descriptor number, so extending it by allowing fork/clone
would make inherited descriptors ambiguous and could cause a child to outlive
the lease owner known to the control server.

## Decision

Phase two introduces an explicit supervisor process table. The first increment
implements process-style `fork()`/`clone(SIGCHLD)`, non-thread `clone3()`
children, and ordinary `CLONE_THREAD` tasks; unusual thread-group teardown
work follows the same model:

- Every traced task has its own pid/tid, syscall-entry state, and pending
  record. Process children have a copied fd table; `CLONE_THREAD` tasks share
  their group's fd table, so descriptor numbers retain Linux thread-group
  semantics.
- Process-style `clone(CLONE_FILES)` children also share the group fd table.
  They do not receive a second lease owner because a close in either task is a
  close in the shared Linux descriptor table.
- Descriptor lifecycle also covers `fcntl(F_DUPFD*)` and `close_range()` so
  static applications do not leave Windows mappings behind after bulk or
  libc-level descriptor cleanup. `SOCK_CLOEXEC` and `CLOSE_RANGE_CLOEXEC`
  keep bindings tracked until exec-time descriptor teardown or an actual
  close; the `PTRACE_EVENT_EXEC` handler releases those leases.
  `CLOSE_RANGE_UNSHARE` is rejected because it changes the calling task's
  descriptor-table ownership model.
- `PTRACE_O_TRACEFORK` and `PTRACE_O_TRACECLONE` attach process-style children
  before they can execute another syscall. The event handler clones inherited
  fd state and issues `ADOPT child-pid lease` once per inherited lease.
- `vfork()` uses `PTRACE_EVENT_VFORK` and a copied process group, so child-side
  `bind/listen` before `_exit` is coordinated. Ordinary `CLONE_THREAD` tasks share the binding
  table and migrate the group owner at `PTRACE_EVENT_EXIT`; unusual exec and
  signal interactions remain follow-up validation.
- Lease teardown will use owner-scoped `RELEASE pid lease`; a lease is closed
  by the control server only after its final owner disappears. `CLOSE` remains
  reserved for a lease with no child owner.
- `CLONE_THREAD` tasks share the process lease owner and fd/binding table but
  keep separate pending-syscall state. Group exit releases the binding table
  only after the final task exits; a leader exit migrates ownership first.
  Tasks marked by `PTRACE_EVENT_EXIT` are excluded from future owner selection,
  preventing ownership from migrating back to a thread that is already dying.
- `vfork()` child-side libc behavior remains outside the contract; direct
  syscall-safe operations through `_exit` are the supported pattern.
- The adapter remains opt-in. Unsupported architectures and unrecognized
  ptrace events fail closed with a diagnostic rather than silently falling back
  to polling.

The control protocol is unchanged; this is an ownership and supervision change
inside the kernel adapter. The default dynamic interposer remains the preferred
path for dynamically linked applications because it already implements the
lower-overhead process lifecycle hooks. Its process-local tracking table cannot
represent shared descriptor tables across process boundaries, so it rejects
process-style `CLONE_FILES` in the raw/libc `clone()` paths. `clone3()` shared
fd creation is checked with a bounded `process_vm_readv` flags read and is
rejected when the dynamic control socket is enabled; unreadable flags fail
closed as `ENOTSUP`. Applications needing shared-fd process creation use this
kernel adapter instead.

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
