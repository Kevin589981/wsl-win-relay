# ADR-0030: Return the Windows-Allocated Listener Address

## Status
Accepted

## Context

Fixed automatic port offsets avoid the mirrored same-port collision but can
still conflict with another Windows listener. Selecting a port in WSL and then
asking Windows to bind it introduces a time-of-check/time-of-use race. Windows
can allocate an available port atomically by binding port zero, but the current
listener acknowledgement does not return the selected address.

## Decision

Add `CapabilityListenBoundAddress`. A client requesting this capability allows
the server to include `listener.Addr().String()` in TCP and UDP listener success
frames. Without the capability, the server sends the legacy empty payload. A
new client receiving an empty payload falls back to the requested address.
Reverse TCP reservations and reverse UDP handles expose `BoundAddress()` after
the success acknowledgement. Keep the core capability set separate so ordinary
new clients remain compatible with peers that predate this extension.

This stage establishes the protocol contract. Automatic allocation policy is
added separately so explicit mappings and strict same-port reservations do not
change behavior implicitly.

## Consequences

- Windows performs free-port selection atomically, without WSL-side probing.
- TCP and UDP share the same negotiated acknowledgement contract.
- Existing peers keep empty listener acknowledgements and fixed-address
  behavior.
- A client that needs dynamic allocation must require the new capability before
  requesting port zero.
