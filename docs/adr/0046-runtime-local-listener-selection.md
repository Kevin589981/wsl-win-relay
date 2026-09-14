# ADR-0046: Select and Publish a Reachable WSL-Local Listener

## Status

Accepted

## Context

Mirrored WSL networking can install policy routes that send `127.0.0.1` through
the synthetic `loopback0` interface instead of the Linux `lo` interface. When
that HNS-managed path fails, a process can successfully bind
`127.0.0.1:<port>` while clients remain in `SYN-SENT` and never reach the
listener. Some WSL installations also assign another local address to `lo`, but
the value is environment-specific and may change across machines or boots.

The SOCKS5 listener, HTTP listener, doctor, and transparent TUN layer must agree
on one actually reachable endpoint. TUN must also reject stale endpoint state
before changing the default routes.

## Decision

Support `auto:<port>` for WSL-local TCP listeners. Automatic selection:

1. enumerates IPv4 addresses assigned to a loopback interface;
2. prefers conventional `127.0.0.1`;
3. requires both a successful bind and a real TCP connect/accept probe;
4. tries other deterministic loopback candidates after a failed probe; and
5. keeps related SOCKS5 and HTTP listeners on the same preferred host when
   possible.

The proxy atomically publishes its effective addresses, process ID, and update
time to a private runtime status file. Transparent TUN `auto` mode accepts that
file only when it is regular, the publisher process is alive, and the exact
published address is listening. A proxy address proven local is dialed through
the kernel's local route without forcing tun2socks onto an external interface.

## Consequences

### Positive

- Broken mirrored localhost routing is detected by behavior, not interface name.
- No WSL-specific alias such as `10.255.255.254` is hardcoded.
- Proxy, doctor, and TUN use one authoritative effective endpoint.
- Stale status fails before privileged route mutation.
- Explicit `host:port` configurations remain compatible.

### Negative

- Automatic startup may take one short probe timeout per broken candidate.
- The runtime status file becomes an operational dependency for TUN auto mode.
- The first release implements automatic IPv4 selection; explicit IPv6 listener
  addresses remain supported but are not auto-selected.

### Neutral

- The selected address is allowed to change after a WSL restart.
- Users should consume the status file or use doctor/TUN auto mode instead of
  assuming a fixed local proxy address.

## Alternatives Considered

**Always use `127.0.0.1`**

Rejected because a successful bind does not prove reachability when mirrored
policy routing diverts localhost traffic.

**Hardcode a known WSL alias**

Rejected because aliases are not guaranteed across hosts, WSL releases, or
boots.

**Create a project-owned loopback alias**

Deferred because it requires root for the non-transparent proxy mode and adds
address allocation and cleanup concerns.

**Expose the proxy on `0.0.0.0`**

Rejected because it did not bypass the broken local route in the observed
failure and would unnecessarily expose an unauthenticated proxy.

## References

- `docs/plans/2026-09-14-reliable-mirror-recovery.md`
- `docs/adr/0009-transparent-routing-via-tun2socks.md`
- `docs/adr/0019-broker-host-supervisor.md`
