# ADR-0042: Share Diagnostic Text Bounds

## Status

Accepted

## Context

Relay errors, attach rejections, automatic-mapping status, and strict control
responses all carry diagnostic strings across a bounded boundary. Their local
implementations did not have identical behavior: most avoided splitting a
multi-byte rune only when truncating, attach errors could split one, and short
invalid input remained invalid UTF-8. Line-oriented protocols also need to
prevent embedded CR/LF from changing message framing.

## Decision

Add `internal/diagnostic` with two byte-budgeted operations:

- `UTF8` replaces invalid input and truncates only at a complete rune;
- `SingleLine` also replaces CR and LF with spaces before applying `UTF8`.

Relay error frames and automatic-mapping status use `UTF8`. Attach rejection
payloads and strict control errors use `SingleLine`. Each consumer continues
to own its protocol-specific maximum and framing bytes.

## Consequences

- All persisted and control-plane diagnostics are valid UTF-8 within their
  declared byte budget.
- Line protocols cannot receive injected diagnostic lines.
- Protocol limits remain local and visible; the helper does not create one
  accidental global maximum for unrelated boundaries.
- New diagnostic boundaries can reuse the same tested normalization contract.
