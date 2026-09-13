# ADR-0041: Bound Strict Control Error Responses

## Status

Accepted

## Context

Strict-listener requests and handler counts were bounded, but errors returned
by a reservation backend were written to the line protocol without a length
limit. The LD_PRELOAD interposer accepts at most 255 wire bytes in its 256-byte
response buffer, while the kernel supervisor accepts 511. A longer response
made the interposer report `EOVERFLOW` instead of the Windows bind errno.
Embedded carriage returns could also create ambiguous diagnostics.

## Decision

Cap every strict control error response at 255 bytes including its prefix and
newline. Replace carriage returns and newlines in backend messages with spaces,
replace invalid UTF-8 with the standard replacement rune, and truncate only at
a complete UTF-8 boundary. The numeric errno and line framing always remain
intact.

The limit is exported as `listencontrol.MaxControlResponseBytes` and is tested
both at the encoder and through the reservation-backend failure path.

## Consequences

- Both native adapters can always parse the intended errno even when a backend
  supplies an oversized or malformed error string.
- One backend error cannot inject additional control-protocol lines.
- Human diagnostics may be truncated, while the numeric errno remains intact
  as the application's authoritative failure result.
