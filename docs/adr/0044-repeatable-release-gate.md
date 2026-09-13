# ADR-0044: Establish a Repeatable Release Gate

## Status

Accepted

## Context

The repository accumulated focused Go, native, installer, recovery, and real
interop checks. Running them as an informal command list made a complete local
verification difficult to reproduce and allowed the GitHub Actions native job
to drift from developer verification. This is particularly harmful while an
account-level Actions billing failure prevents hosted jobs from starting.

Some checks are deterministic and isolated on any amd64 Linux/WSL host. Others
must launch Windows executables and may depend on a configured Windows-side
upstream proxy, so they cannot be mandatory on ordinary Linux CI runners.

## Decision

Add `scripts/test-release.sh` as the fail-fast release gate. Its default mode
runs Go tests, vet, race detection, the amd64 cross-platform build, metadata,
native adapters, transparent rollback, isolated installers, and every
Linux-hosted broker recovery smoke. `--windows-interop` additionally runs the
stdio auto-rebind and full Windows broker interop checks using the caller's
existing environment.

The CI native job invokes `--native-only` because the separate Go matrix and
race jobs already own those checks. Shell syntax uses controlled globs so new
shell scripts are automatically included. Arm64 remains build-only in its
separate CI job and is not claimed as runtime-verified.

## Consequences

- One command can provide reproducible evidence for a release candidate.
- CI and local native verification share one ordered test manifest.
- Real Windows/HNS-independent verification remains explicit and can preserve
  machine-specific PowerShell and upstream-proxy configuration.
- The full gate is intentionally slower than focused package tests and should
  run at stage/release boundaries rather than after every edit.
