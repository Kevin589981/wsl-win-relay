# Reliable Mirror Recovery Implementation Plan

> **For Codex:** Execute this plan task by task, keeping the broker lifecycle and local endpoint changes independently testable.

**Goal:** Make relay recovery reliable when WSL restarts while an older Windows broker remains alive, and automatically select a reachable WSL-local proxy address when mirror mode breaks `127.0.0.1`.

**Architecture:** Keep the existing broker/connector protocol, but add a private supervisor election endpoint so only one broker supervisor owns a configured endpoint. Add an `auto:<port>` local listener mode that enumerates WSL loopback-interface addresses and accepts only an address that passes both bind and connect checks. Publish the selected proxy endpoint in a small runtime status file so transparent TUN startup can consume the same address without hardcoded aliases; local proxy dialing must not be forced through an external uplink interface.

**Tech Stack:** Go 1.22, Unix/Windows local IPC (`go-winio` on Windows), POSIX shell, Linux `iproute2`/TUN, existing `tun2socks` integration, Go tests and shell integration tests.

---

### Task 1: Capture the failure contract

**Files:**
- Modify: `docs/adr/` only if a new decision record is required after implementation.
- Test: existing broker and transparent-relay integration scripts.

**Steps:**
1. Record the observed failure sequence: stale Windows broker, duplicate frontend bind, connector endpoint appears ready but requests stall, and mirror policy routing sends `127.0.0.1` through `loopback0`.
2. Define acceptance criteria: one active supervisor per endpoint, no duplicate role tree after WSL restart, local proxy endpoint selection is verified by a real TCP connect, and TUN does not bind a local proxy dialer to `eth*`.
3. Preserve compatibility for explicit addresses such as `127.0.0.1:1080` and `10.255.255.254:1081`.

### Task 2: Make broker supervisor ownership exclusive

**Files:**
- Modify: `cmd/win-broker/supervisor.go`
- Modify: `cmd/win-broker/main_test.go`
- Test: `scripts/test-broker-supervisor.sh`

**Steps:**
1. Add a private, per-endpoint supervisor election listener.
2. Make a second supervisor wait as standby instead of creating a competing frontend.
3. Probe the public frontend and wait for it to disappear before takeover.
4. Add unit and integration coverage for duplicate supervisors and takeover after owner exit.
5. Verify native Linux tests and Windows cross-compilation.

### Task 3: Add automatic WSL-local listener selection

**Files:**
- Create: `internal/listenaddr/listenaddr.go`
- Create: `internal/listenaddr/listenaddr_test.go`
- Modify: `internal/config/config.go`
- Modify: `cmd/wsl-proxy/main.go`
- Modify: `cmd/wsl-proxy/main_test.go`

**Steps:**
1. Define the `auto:<port>` listener specification while retaining ordinary `host:port` specifications.
2. Enumerate IPv4 addresses on Linux loopback interfaces, preferring `127.0.0.1` and then other addresses assigned to `lo`; never select a non-loopback interface for local proxy listeners.
3. For each candidate, require successful `net.Listen` and a real same-namespace TCP connect/accept probe before selecting it.
4. Return the actual selected address to the proxy runtime and log it.
5. Keep HTTP proxy selection coupled to the SOCKS selection when both are configured as `auto`, so TUN and diagnostics have one authoritative endpoint.
6. Add deterministic tests with injectable candidate and probe functions, including a failed `127.0.0.1` candidate followed by a successful loopback alias.

### Task 4: Publish the selected endpoint for recovery tools

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/wsl-proxy/main.go`
- Modify: `cmd/wsl-proxy/main_test.go`
- Modify: `scripts/run-user-service.sh` only if service environment propagation is needed.

**Steps:**
1. Add an optional private runtime status path for the selected SOCKS/HTTP addresses, defaulting to a per-user runtime location.
2. Write the status atomically after listeners are ready and remove or replace it during shutdown.
3. Ensure stale status cannot be mistaken for a live listener by requiring the TUN-side connect probe.
4. Keep status contents non-secret and protect the file from symlink/path substitution.

### Task 5: Make transparent TUN consume local endpoints safely

**Files:**
- Modify: `scripts/transparent-relay.sh`
- Modify: `scripts/test-transparent-relay.sh`
- Modify: `systemd/wsl-win-relay-transparent.env`

**Steps:**
1. Allow TUN startup to resolve a local proxy endpoint from the runtime status file when the configured proxy host is loopback or `auto`.
2. Probe the selected endpoint before changing routes.
3. Do not pass `--interface <uplink>` to tun2socks for any proxy address proven local by the kernel; use the normal loopback route.
4. Preserve the external-uplink binding behavior for non-local proxy addresses.
5. Add tests for `127.0.0.1` failure plus loopback-alias fallback, local-proxy argument construction, proxy disappearance rollback, and route rollback.

### Task 6: Validate recovery and update documentation

**Files:**
- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Modify: `docs/plans/2026-09-14-reliable-mirror-recovery.md` with final verification notes.

**Steps:**
1. Run `go test ./...`, race tests where practical, broker supervisor integration tests, and transparent relay tests.
2. Cross-build the Windows broker and verify build metadata matches the installed connector.
3. Reinstall the fixed Windows broker and WSL scripts in the configured local paths.
4. Verify a duplicate supervisor enters standby, the active services remain healthy, and a real SOCKS/HTTP request succeeds through the Windows upstream proxy.
5. Document that `10.255.255.254` is an observed WSL-local alias, not a universal constant, and explain automatic endpoint selection and broker takeover behavior.
6. Commit and push the implementation and documentation together.

## Verification Notes

- `go test ./...`, `go vet ./...`, and `go test -race ./...` passed in the
  affected WSL instance.
- `scripts/test-release.sh` passed, including native strict adapters,
  transparent rollback, broker reconnect, automatic mapping recovery, process
  recovery, duplicate supervisor exclusion, and simulated WSL shutdown with a
  surviving Windows broker tree.
- The broker integration tests now run when mirror mode breaks
  `127.0.0.1`; they select a reachable WSL-local address instead of skipping
  those environments.
- Real Windows interop passed against a temporary Windows-local HTTP target,
  covering SOCKS5, HTTP proxying, named-pipe attach, connector restart,
  explicit reverse forwarding, and automatic forwarding without relying on an
  external proxy node.
- The configured external proxy was independently observed returning TLS and
  empty-response failures during final testing, so internet reachability is
  treated as an external health signal rather than a release gate for the relay.
