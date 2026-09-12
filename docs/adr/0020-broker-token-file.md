# ADR-0020: Accept Broker Credentials From a Protected Token File

## Status

Accepted

## Context

The broker supervisor and its internal roles need the same attach token. Passing
the token as `-token-hex` exposes it in process listings and task definitions,
which is undesirable when the broker is moved behind a Windows host service.
The existing environment-variable path must remain compatible with WSLENV and
the current installer.

## Decision

`wsl-win-broker` accepts `-token-file`. The file must be a regular, non-symlink
file; on Unix it must not grant group/world permissions. Its trimmed contents
are decoded as hexadecimal and become the canonical token. A token file takes
precedence over `-token-hex` and `WSL_WIN_RELAY_ATTACH_TOKEN` when both are
present.

When a broker frontend starts worker, socket-host, or socket-owner children, it
passes the token-file path instead of the token contents. The host supervisor
does the same for the frontend child. Existing `-token-hex` invocations remain
valid for compatibility and tests.

The WSL systemd broker installer creates a mode-0600 `attach.token` beside its
private environment and passes that file to the Windows broker. The environment
continues to carry the token value only because WSL connector children need it
through `WSLENV`; existing installations without the file retain a legacy
`-token-hex` service fallback. The wrapper converts Linux paths to Windows paths
with `wslpath -w` before starting a Windows executable.

The file path itself is not treated as a secret; deployments must protect the
file with the host's normal user ACLs. Windows ACL enforcement remains a
deployment responsibility because POSIX mode bits are not reliable on NTFS.

## Consequences

- Process listings no longer need to contain the broker token when deployments
  use a token file.
- A host task/service can reference one protected file while WSL connector
  credentials continue to flow through the existing private environment.
- Moving or deleting the token file prevents role restart until the deployment
  restores it, which is preferable to silently falling back to a stale token.
