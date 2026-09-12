# ADR-0023: Fail Closed for Unsupported Strict-Listener Targets

## Status

Accepted

## Context

`LD_PRELOAD` is ignored for statically linked and setuid/setgid executables.
Launching such a program through the strict-listen wrapper therefore cannot
enforce the Windows-before-WSL bind contract. Silently running it would be
more dangerous than rejecting it because the application could believe its
port was coordinated when it was not.

## Decision

`wsl-win-relay-run` performs a best-effort target preflight. When the direct
target is an ELF file, it rejects targets without a `PT_INTERP` program header
(static or static-PIE) and rejects files whose mode contains setuid/setgid
bits. Non-ELF commands and environments without `readelf` retain the existing
launcher behavior; the interposer remains responsible for runtime checks.

The preflight is diagnostic only and does not claim kernel-level coverage for
static binaries or arbitrary process launchers. Such applications must use the
polling adapter, a source-level wrapper, or a future kernel-aware adapter.

## Consequences

- A supported strict-launch invocation cannot silently degrade for the common
  directly-executed static/setuid target cases.
- Scripts and interpreters remain usable because non-ELF entrypoints are not
  classified as static applications.
- Full static-binary coverage remains an explicit future kernel-boundary task.
