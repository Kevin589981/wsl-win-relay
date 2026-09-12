# ADR-0017: Propagate Broker Credentials Through WSLENV

## Status

Accepted

## Context

Broker mode launches the Windows connector as a child of the WSL proxy. WSL
interop does not export arbitrary Linux environment variables to a Windows
process. The connector therefore cannot reliably read
`WSL_WIN_RELAY_BROKER_ENDPOINT` or `WSL_WIN_RELAY_ATTACH_TOKEN` unless those
names are listed in `WSLENV`.

The proxy must also work when a user's existing `WSLENV` contains stale entries
for either broker variable. In particular, the `/u` flag means that a value is
unset on the translated side, which would make an otherwise valid connector
fail authentication.

## Decision

When broker mode starts a Windows connector, the WSL proxy constructs the child
environment from the current environment and normalizes `WSLENV` as follows:

- preserve unrelated entries and their flags;
- remove duplicate broker-variable entries, matching their names before `/`;
- add exactly one flag-free entry for each broker variable.

The token remains an environment value and is never placed in connector
arguments. The connector and broker continue to validate the token as a
per-user attach secret over local IPC.

## Consequences

Broker mode works without requiring users to edit their global `WSLENV`.
Existing `WSLENV` settings remain intact except for the two reserved relay
names. A user who intentionally needs custom translation flags for those names
must configure a separate wrapper or environment boundary; the relay always
requires the values to be visible to the Windows connector.

This is specific to WSL interop process launch. Native Linux stdio mode does not
modify `WSLENV`, and the broker environment file remains mode `0600` so the
token is not exposed through a world-readable configuration file.

## Alternatives considered

1. Put the token in connector command-line arguments. Rejected because process
   listings and diagnostics can expose command-line secrets.
2. Require users to preconfigure `WSLENV` globally. Rejected because it makes
   installation fragile and leaves stale `/u` entries undetected.
3. Use a temporary file or disk tunnel for credentials. Rejected because it
   adds lifecycle and cleanup state without improving the local IPC trust
   boundary.
