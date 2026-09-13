# ADR-0003: Separate Outbound Dialing from Inbound Port Forwarding

## Status
Accepted

## Context

Outbound traffic from WSL only needs a destination. Windows can create a normal WinSock connection with an ephemeral source port; there is no useful requirement for the source port to match a WSL port.

Inbound traffic has a different ownership model. A Windows client can connect only if a Windows socket is already listening. When HNS or localhost forwarding is unavailable, an explicit relay listener is required. A user-space WSL helper cannot passively discover an arbitrary application `listen(2)` call and then bind the same WSL port without either owning the port first or using kernel-level transparent interception.

## Decision

Implement reverse forwarding as an explicit mapping:

```text
Windows 0.0.0.0:8000 -> relay -> WSL 127.0.0.1:8000
```

The Windows relay owns the external listener. Each accepted connection becomes a multiplexed inbound stream. The WSL side dials the configured local destination and joins the two streams. Listener registration, accepted-stream creation, and listener rejection are explicit protocol operations.

TCP polling discovery is implemented in ADR-0004, strict same-port
synchronization in ADR-0005 and ADR-0025, allowlisted UDP discovery in
ADR-0013, and transparent TCP/UDP routing in ADR-0009. These remain adapters;
none is built into the core relay protocol assumptions.

## Consequences

### Positive

- Local WSL applications keep their existing listening port.
- Windows bind failures can be reported back to WSL immediately.
- Outbound and inbound lifecycles remain independently testable.
- The same transport works when HNS and WSL loopback forwarding are broken.

### Negative

- A mapping must be configured before Windows clients connect.
- Windows port conflicts and firewall policy still apply.
- Unrestricted UDP discovery remains unsafe because procfs cannot distinguish
  every server from an ephemeral client; explicit and allowlisted modes cover
  the supported cases.
