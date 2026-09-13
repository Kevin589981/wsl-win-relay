# ADR-0045: Normalize DrvFS Build Version Detection

## Status

Accepted

## Context

The repository is intentionally usable from both Windows and WSL. Windows Git
may check out selected text files with CRLF under `core.autocrlf=true`, while
WSL Git reading the same DrvFS worktree defaults to LF semantics. The Windows
worktree can therefore be clean while `git describe --dirty` from WSL reports
platform-only line-ending changes. Release binaries then carry a misleading
`-dirty` suffix even though their source commit is exact.

## Decision

Derive build version and commit metadata with a command-local
`core.autocrlf=true`. This normalizes CRLF/LF changes for Git's worktree
comparison without changing repository or user configuration. Actual content
changes still produce the dirty suffix.

The metadata smoke independently computes the normalized worktree description
and requires the embedded version to match it. Explicit build metadata
overrides retain their existing precedence for reproducible CI builds.

## Consequences

- A clean checkout shared by Windows and WSL produces a clean commit version.
- Local Git configuration is neither read as policy nor modified.
- Line-ending-only differences are intentionally not treated as source dirt;
  semantic edits remain visible.
- CI-provided version/commit overrides continue to be deterministic.
