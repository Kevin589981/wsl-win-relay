# ADR-0026: Architecture Adapters for the Kernel Supervisor

## Status

Accepted, amd64 and aarch64 adapters implemented

## Context

The ptrace supervisor originally used amd64-only `struct user_regs_struct`
fields directly. That made the source impossible to build on arm64 even though
the relay protocol and syscall policy are architecture-neutral. WSL deployments
can run on arm64 hosts, and the full adapter must not encode register-layout
assumptions in its lifecycle logic.

## Decision

Keep one supervisor implementation and isolate register access in
`native/strict_supervisor_regs.h`:

- amd64 uses `PTRACE_GETREGS`/`PTRACE_SETREGS` and the conventional syscall
  registers (`orig_rax`, `rax`, `rdi`...`r9`).
- aarch64 uses `PTRACE_GETREGSET`/`PTRACE_SETREGSET` with `NT_PRSTATUS`; `x8`
  carries the syscall number, `x0` the return value, and `x0` through `x5` the
  syscall arguments.
- Unsupported architectures fail at compile time with an explicit diagnostic
  instead of silently building a supervisor that cannot enforce leases.

The syscall policy, process/group state machine, lease protocol, and launcher
remain shared. Runtime validation on a native aarch64 WSL kernel is a separate
test target; amd64 remains the continuously exercised CI target.

## Consequences

- Architecture-specific ptrace details are limited to one small header.
- Adding another architecture requires only its register adapter plus native
  runtime tests, not a second lifecycle implementation.
- Cross-architecture CI is still needed before claiming production support on
  arm64.

