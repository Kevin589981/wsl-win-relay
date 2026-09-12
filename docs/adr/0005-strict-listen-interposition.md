# ADR-0005: Offer Strict Listen Coordination Through LD_PRELOAD

## Status
Accepted

## Context

Polling can discover a WSL listener only after Linux has returned success. It cannot satisfy the stronger contract that a Windows bind failure also makes the WSL application's `listen(2)` fail.

Linux dynamically linked applications can opt into function interposition
without kernel changes. The relay protocol now supports reserving a
bound-but-not-accepted Windows listener and committing it only after Linux
begins listening. The interposer also handles direct `syscall(2)` calls for the
two relevant syscall numbers when the application is dynamically linked.

## Decision

Provide `libwsl_win_relay_listen.so` and a `wsl-win-relay-run` launcher. The interposer:

1. Intercepts TCP `listen()` and non-zero UDP `bind()` through libc symbols and
   direct `syscall(SYS_listen/SYS_bind)` calls.
2. Requests a Windows listener reservation over a mode-`0600` Unix socket.
3. Returns the Windows error to the application if reservation fails.
4. Calls the real Linux `listen()` only after Windows succeeds.
5. Commits Windows TCP accepting after Linux succeeds, or aborts on Linux
   failure. UDP mappings are opened before Linux `bind()` because datagrams do
   not have an accept queue; a failed Linux bind closes the Windows mapping.
6. Tracks `dup()`, `dup2()`, `dup3()`, and `fcntl(F_DUPFD*)` aliases, and
   intercepts `close_range()` when it actually closes descriptors; the Windows
   mapping is released only after the final alias in a process is closed.
7. Registers inherited leases for ordinary `fork()` and process-style
   `clone()` and `clone3()` process children and releases a mapping only after
   every process owner has gone away.
8. Releases abandoned mappings through the control daemon's process-identity
   lease reaper when an owner exits without callbacks.
9. Retries only transient control-socket availability errors for a bounded
   two-second startup/recovery window by default; the
   `WSL_WIN_RELAY_CONTROL_RETRY_SECONDS` environment variable can extend it to
   sixty seconds. Definitive Windows bind errors are returned immediately.

## Consequences

### Positive

- Windows and WSL bind success is coordinated before the application observes success.
- Incoming Windows connections are not accepted before the WSL service is ready.
- Existing dynamically linked applications need no source changes.

### Negative

- Static and setuid binaries are not interposed. Direct raw syscalls are
  covered for dynamically linked programs, but a statically linked program
  still requires a kernel-aware adapter.
- Descriptor duplication through the standard `dup*()` calls,
  `fcntl(F_DUPFD*)`, ordinary `fork()`, and process-style `clone()`/`clone3()`
  are covered. `CLONE_THREAD` and `vfork()` ownership semantics remain outside
  the interposer contract. The current native build is Linux amd64, matching
  the supported WSL binary target.
- Crash cleanup depends on daemon-side lease reaping rather than a `close()`
  callback.
- A `listen()` call can wait up to two seconds when the relay control service
  is restarting or has not started yet.
- UDP `bind()` calls using port zero are deliberately excluded so ephemeral
  client sockets are not exposed as Windows listeners.

## Alternatives Considered

**Polling only:** Broad compatibility, but cannot propagate Windows rejection into the original syscall.

**Kernel module:** Can cover every syscall but significantly increases installation, security, signing, and kernel-version maintenance costs.

**ptrace/seccomp supervisor:** More complete than LD_PRELOAD but adds process supervision, performance overhead, and substantially more failure modes.
