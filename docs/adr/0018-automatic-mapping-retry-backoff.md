# ADR-0018: Back Off Rejected Automatic Mappings

## Status

Accepted

## Context

Procfs discovery runs continuously. A Windows bind refusal can persist because
another process owns the port, a firewall policy is intentional, or the relay
session is still recovering. Retrying every scan wastes relay requests and can
turn one expected conflict into sustained log and CPU load.

## Decision

Keep rejection state per discovered address-family/port and retry it with a
bounded exponential delay. The default delay starts at one second and doubles
up to thirty seconds. Deployments may override the lower and upper bounds with
`auto_forward.retry_min`/`auto_forward.retry_max` or the equivalent CLI flags;
the maximum cannot be lower than the minimum. A successful mapping clears the
failure count and emits the existing restoration log. Removing the WSL
listener, resetting the relay session, or shutting down the watcher clears the
rejection state so a new session is retried immediately.

Cancellation and stale-generation results are not recorded as rejections. The
policy remains local to the watcher; the values only control retry scheduling
and do not change the Windows bind or WSL listener contract.

## Consequences

- Persistent Windows conflicts no longer cause a bind attempt on every scan.
- A port that becomes available is eventually retried without restarting WSL.
- Relay replacement still causes immediate reconstruction through `Reset`.
- The first retry can be delayed by at most the configured scan interval plus
  the one-second minimum; strict interposition remains the mechanism for
  synchronous bind-error propagation.
