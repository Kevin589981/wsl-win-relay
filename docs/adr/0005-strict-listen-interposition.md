# ADR-0005: Offer Strict Listen Coordination Through LD_PRELOAD

## Status
Accepted

## Context

Polling can discover a WSL listener only after Linux has returned success. It cannot satisfy the stronger contract that a Windows bind failure also makes the WSL application's `listen(2)` fail.

Linux dynamically linked applications can opt into function interposition without kernel changes. The relay protocol now supports reserving a bound-but-not-accepted Windows listener and committing it only after Linux begins listening.

## Decision

Provide `libwsl_win_relay_listen.so` and a `wsl-win-relay-run` launcher. The interposer:

1. Intercepts TCP `listen()`.
2. Requests a Windows listener reservation over a mode-`0600` Unix socket.
3. Returns the Windows error to the application if reservation fails.
4. Calls the real Linux `listen()` only after Windows succeeds.
5. Commits Windows accepting after Linux succeeds, or aborts on Linux failure.
6. Tracks `dup()`, `dup2()`, and `dup3()` aliases and releases the Windows
   mapping only after the final alias in a process is closed.
7. Registers inherited leases for ordinary `fork()` children and releases a
   mapping only after every process owner has gone away.
8. Releases abandoned mappings through the control daemon's process-identity
   lease reaper when an owner exits without callbacks.

## Consequences

### Positive

- Windows and WSL bind success is coordinated before the application observes success.
- Incoming Windows connections are not accepted before the WSL service is ready.
- Existing dynamically linked applications need no source changes.

### Negative

- Static binaries, setuid binaries, and programs that bypass libc are not interposed.
- Descriptor duplication through the standard `dup*()` calls and ordinary
  `fork()` are covered. `clone()` and `vfork()` ownership semantics remain
  outside the interposer contract.
- Crash cleanup depends on daemon-side lease reaping rather than a `close()`
  callback.

## Alternatives Considered

**Polling only:** Broad compatibility, but cannot propagate Windows rejection into the original syscall.

**Kernel module:** Can cover every syscall but significantly increases installation, security, signing, and kernel-version maintenance costs.

**ptrace/seccomp supervisor:** More complete than LD_PRELOAD but adds process supervision, performance overhead, and substantially more failure modes.
