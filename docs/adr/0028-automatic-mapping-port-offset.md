# ADR-0028: Add an Explicit Port Offset for Automatic Mappings

## Status
Accepted

## Context

Mirrored WSL networking can make WSL and Windows share one effective socket
namespace. In that topology a polling automatic mapping that binds the same
Windows port as its WSL source may be refused even though the relay transport
is healthy. Silently selecting a different port would make discovery
unpredictable and would break clients that rely on the documented mapping.

## Decision

Add an opt-in integer `auto_forward.windows_port_offset` configuration field
and `-auto-forward-port-offset` CLI flag. The default is zero, preserving the
existing same-port contract. For a discovered WSL listener on port `P`, the
Windows mapping binds `P + offset` while its reverse target remains `P`.
Validate configured offsets between `-65534` and `65534`, and reject an
individual mapping when the resulting Windows port is outside `1..65535`.
Apply the same policy to allowlisted UDP discovery.

## Consequences

- Mirrored deployments can choose a stable alternate Windows listener range.
- The source WSL service and relay target remain unchanged, so application
  behavior inside WSL is not coupled to the Windows-facing port.
- The alternate port must be communicated to Windows clients; automatic
  discovery does not provide a universal port translation registry.
- Strict synchronized listen remains the mechanism for synchronous refusal;
  this offset only changes polling automatic mapping behavior.
