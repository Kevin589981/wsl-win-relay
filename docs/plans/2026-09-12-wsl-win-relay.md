# WSL-Windows Relay Implementation Plan

**Goal:** Build a durable emergency network relay that exposes a SOCKS5 proxy in WSL while a Windows process performs outbound TCP connections.

**Architecture:** A versioned multiplexed frame protocol runs over the Windows process stdin/stdout pipes. The WSL process owns the local proxy listener and relay client; stdio mode keeps relay state in the Windows child, while broker mode moves it into a persistent socket owner behind replaceable frontend, bridge-worker, and socket-host bridge processes. Protocol, transport, relay, and proxy layers are separate so later transparent TCP/UDP adapters can reuse the relay.

**Tech Stack:** Go standard library, cross-compiled Windows/Linux binaries, `go test`.

## Current Completion Matrix

- [x] Framed stdio relay with bounded streams and capability negotiation.
- [x] Control error frames and persisted automatic-mapping errors are bounded
      at 4 KiB so a malformed peer cannot amplify diagnostic text into large
      logs or status snapshots.
- [x] Every executable exposes consistent version, commit, and build-time
      metadata; the WSL build and CI paths inject and verify matching values.
- [x] SOCKS5 TCP CONNECT and UDP ASSOCIATE, plus optional HTTP CONNECT and
      absolute-form cleartext HTTP forwarding.
- [x] JSON configuration uses the accurate `http_proxy_listen` name for the
      HTTP frontend while normalizing the legacy `http_connect_listen` alias
      and rejecting conflicting values.
- [x] SOCKS5/HTTP client handshakes are bounded by a shared configurable
      timeout; HTTP CONNECT headers are capped at 64 KiB and proxy shutdown
      closes accepted clients and drains their handlers.
- [x] Plain HTTP proxy clients can send sequential absolute-form requests on
      one connection; each request uses a fresh origin connection and response
      hop-by-hop headers are removed before returning it to the client. Every
      request header block remains capped at 64 KiB, including later requests
      after parser read-ahead.
- [x] Windows-side HTTP CONNECT and SOCKS5/SOCKS5H upstream support.
- [x] Explicit TCP and UDP reverse forwarding with bind-error propagation.
- [x] Reverse UDP source flows use connected WSL sockets, preventing an
      unrelated local UDP sender from entering a Windows client's response
      path.
- [x] Polling TCP listener discovery with include/exclude policies.
- [x] Strict TCP `listen()` and non-zero UDP `bind()` coordination through the
      native launcher/interposer, including descriptor ownership cleanup.
- [x] Direct `syscall(SYS_listen/SYS_bind)` coordination for dynamically linked
      Linux amd64 applications.
- [x] Lease adoption for process-style `clone()` children, in addition to
      ordinary `fork()`.
- [x] Parent-side lease adoption for dynamically linked raw `SYS_clone` and
      `SYS_clone3` process children.
- [x] Explicitly allowlisted UDP polling for common unconnected services, with
      a documented procfs ambiguity boundary.
- [x] Process-boundary recovery that recreates mappings after broken relay
      transports.
- [x] Opt-in TUN/tun2socks transparent TCP/UDP routing, including real WSL
      verification while HNS had removed the default route.
- [x] Optional root systemd service installation makes transparent routing
      boot-persistent, deploys the pinned adapter to a stable path, and
      preserves private operator configuration across idempotent upgrades.
- [x] Transparent service uninstall stops and disables the unit, removes only
      fixed deployed executables/unit paths, rejects symlink targets, and
      preserves private routing policy for later recovery.
- [x] Transparent routing waits for a configured loopback SOCKS listener before
      any TUN, route, or DNS mutation, preventing service startup ordering from
      creating a temporary network blackhole.
- [x] A runtime loopback-proxy watchdog tolerates short recovery windows but
      exits through bounded tun2socks termination and full network rollback
      after a configurable sustained listener outage.
- [x] Transparent relay shutdown handles `HUP`/`QUIT` and bounds tun2socks
      termination before route, DNS, and TUN rollback; a mock fault-injection
      smoke runs this path without requiring root or a real TUN device.
- [x] Transparent relay DNS replacement accepts an injectable `WWR_RESOLV_CONF`
      path and restores both symlink shape and original contents in the smoke
      test, keeping container and namespace verification off the host resolver.
- [x] systemd user-service installer and private configuration handling.
- [x] `wsl-proxy -check-config` runs the production option parser without
      starting a relay or binding ports; the user-service installer uses it to
      reject invalid upgrades before touching running services.
- [x] An installed read-only doctor checks configuration permissions and
      parsing, broker credentials, token consistency, Windows interop/build
      compatibility, user-service state, and an optional end-to-end SOCKS
      probe without exposing private token or upstream-proxy values.
- [x] The user-service installer deploys the kernel strict supervisor alongside
      its launcher, so installed static-target coordination does not depend on
      paths inside the source checkout; an isolated installer smoke verifies
      the binary and protected configuration deployment.
- [x] The installer provides a strict-shell wrapper that launches an entire
      shell process tree under the kernel supervisor, allowing child commands
      to inherit synchronous listener coordination without per-command setup.
- [x] Independent IPv4/IPv6 Windows bind hosts for automatic and strict
      listener mappings.
- [x] Configurable Windows port offsets for automatic mappings, preserving the
      WSL listener port while avoiding mirrored shared-namespace collisions.
- [x] Optional atomic JSON status publication for automatic mappings, including
      the actual Windows-facing address when a port offset is configured.
- [x] Automatic mapping status has a low-frequency liveness heartbeat and a
      shared bounded schema reader, so consumers can reject stale crash residue
      using both publisher PID and timestamp without adding a network endpoint.
- [x] Mapping status includes active/rejected state, last bind error, and retry
      time; rejected-only state and backoff are cleared when a listener
      disappears or changes identity.
- [x] Capability-negotiated TCP and UDP listener acknowledgements can return
      the address actually allocated by Windows while retaining empty-payload
      compatibility with older peers.
- [x] Automatic TCP and allowlisted UDP mappings can request collision-free
      Windows-allocated ports; actual addresses are retained through mapping
      lifecycle state and exposed through logs/status publication.
- [x] Session-independent local SOCKS5/HTTP listeners with a reconnecting
      dialer for new requests during relay restarts.
- [x] Strict control socket and live listener leases survive relay child
      replacement; Windows reservations are rebound and recommitted for the
      replacement session.
- [x] Strict control requests have a bounded initial read, and service shutdown
      closes accepted connections, drains handlers, and preserves a replaced
      Unix socket path.
- [x] Automatic TCP and allowlisted UDP mappings are process-scoped and
      rebound through the session-aware dialer after relay child replacement.
- [x] Generation-safe attach registry with per-instance token authentication
      and stale connector teardown isolation (transport foundation for the
      persistent Windows broker).
- [x] Versioned attach-control wire handshake with bounded framing,
      capability exchange, token authentication, and generation acknowledgements.
- [x] Stable registry-entry summary and resume-ack codecs with deterministic
      ordering and bounded entry counts.
- [x] Attach/resume handshake flow exchanges the summary and validates the
      connector's epoch and acknowledged stable IDs before session activation.
- [x] Per-user local IPC transport abstraction with `0600` Unix-socket
      enforcement and Windows named-pipe security descriptor.
- [x] Transport-independent broker core with stable entry registration,
      summary snapshots, attach/resume acceptance, and stale-session isolation.
- [x] Broker listener lifecycle with active connector tracking, cancellation
      teardown, and handler draining before service return.
- [x] The installed broker service wrapper propagates an optional Windows-side
      upstream proxy through `WSLENV`, keeping the URL out of the broker command
      line while preserving private environment-file permissions.
- [x] Opt-in broker and connector executables bridge stdio to local IPC and
      run the existing relay server after attach; offline WSL SOCKS5 integration
      is covered by `scripts/test-broker-connector.sh`.
- [x] Real WSL-to-Windows broker interop smoke builds the Windows binaries,
      exports broker credentials through `WSLENV`, attaches over named pipes,
      reaches external HTTPS and plain HTTP endpoints through SOCKS5/HTTP
      proxy frontends, and verifies Windows PowerShell requests through both
      an explicit reverse mapping and a procfs-discovered automatic mapping
      into temporary WSL HTTP services. The smoke uses a distinct WSL loopback
      alias so mirrored networking does not turn the verification into an
      intentional same-address bind conflict, and verifies that the automatic
      Windows listener is removed when its WSL service exits. It also kills the
      connector once and verifies reattach plus continued explicit and
      automatic mapping reachability. Optional Windows-allocated TCP and
      allowlisted UDP paths read the merged status document, exercise both
      protocols from PowerShell after connector recovery, and verify mapping
      removal. A service-wrapper mode covers the installed
      `broker.env`/`-supervise` startup path and upstream proxy propagation;
      the full combination has passed against
      `socks5h://matebookxpro.local:7890` with Windows-allocated TCP/UDP
      mappings and connector recovery enabled.
- [x] Frame-aware replaceable Link and `relay.Server.ServeAttached` keep the
      broker server lifecycle alive across connector transport replacement.
- [x] Relay client `RunAttached`/`Rehandshake` reuse the same stream registry,
      and retry detach-sensitive frame writes; in-memory TCP streams survive
      both-end transport replacement.
- [x] Non-zero normal Windows relay exit status is classified as fatal instead
      of causing an unbounded restart loop.
- [x] Kernel-supervisor parent-side `vfork()` lease adoption is covered by the
      native lifecycle smoke test; child-side pre-exec networking remains
      unsupported by the shared-address-space contract.
- [x] Dynamic `fork()` inheritance uses a close-on-exec gate: the child cannot
      return to application code until the parent has completed `ADOPT`, and a
      rejected adoption terminates the child before it can use the inherited
      listener.
- [x] Dynamic libc callback-style process `clone()` uses the same pre-callback
      gate; raw process-style clone syscalls and shared-address-space clone
      variants are rejected while tracked listeners exist.
- [x] The kernel supervisor explicitly handles the `PTRACE_EVENT_VFORK_DONE`
      notification emitted for traced `vfork()` parents instead of treating it
      as an unknown event.
- [x] The static lifecycle smoke covers `wordexp()` command substitution,
      exercising a shell-backed libc process launch and descendant lease
      cleanup in addition to `system()` and `popen()`.
- [x] The static lifecycle smoke covers `posix_spawnp()` PATH lookup in
      addition to direct `posix_spawn()` and file-action variants, including
      inherited listener lease adoption and cleanup.
- [x] The static lifecycle smoke covers the explicit
      `POSIX_SPAWN_USEVFORK` attribute path, verifying the shared-address-space
      libc spawn variant through the same traced ownership and cleanup flow.
- [x] The static lifecycle smoke covers the combined `posix_spawnp()` PATH
      lookup and `POSIX_SPAWN_USEVFORK` path, verifying both libc behaviors in
      one child lifecycle.
- [x] The static lifecycle smoke covers `posix_spawnp()` PATH lookup with an
      `addopen()` file action, extending file setup coverage to the PATH-search
      libc entry point.
- [x] The static lifecycle smoke covers `posix_spawnp()` PATH lookup with
      `adddup2()`/`addclose()` actions, verifying inherited lease adoption while
      the child rewires a descriptor before exec.
- [x] The static lifecycle smoke covers `posix_spawn` signal-mask,
      signal-default, and process-group attributes, extending non-direct libc
      vfork coverage to common pre-exec process state setup.
- [x] The static lifecycle smoke covers the GNU `POSIX_SPAWN_SETSID` session
      attribute when provided by the host libc, with an explicit skip otherwise.
- [x] The static lifecycle smoke covers the standard `POSIX_SPAWN_RESETIDS`
      attribute when provided by the host libc, preserving lease tracking while
      spawn resets child identity attributes.
- [x] The static lifecycle smoke covers GNU `posix_spawn` `addchdir_np` and
      `addfchdir_np` file actions when the host glibc provides them, with an
      explicit skip on older libc versions.
- [x] The static lifecycle smoke covers `posix_spawnp()` PATH lookup combined
      with GNU `addchdir_np`/`addfchdir_np` actions, extending pre-exec working
      directory coverage to the PATH-search libc entry point.
- [x] The static lifecycle smoke covers the combined
      `posix_spawnp(POSIX_SPAWN_USEVFORK)` and `addopen()` path, verifying
      PATH-search file actions under the shared-address-space spawn variant.
- [x] The static lifecycle smoke covers `posix_spawn(POSIX_SPAWN_USEVFORK)`
      with `adddup2()`/`addclose()` actions, extending descriptor ownership
      coverage through a vfork child.
- [x] The static lifecycle smoke covers GNU `posix_spawn` `addclosefrom_np`,
      verifying child-side bulk descriptor cleanup while the parent lease
      remains live; older glibc versions skip this optional action explicitly.
- [x] The static lifecycle smoke covers `posix_spawnp()` PATH lookup with
      GNU `addclosefrom_np`, extending bulk descriptor cleanup coverage to the
      PATH-search libc entry point; older glibc versions skip this optional
      action explicitly.
- [x] The static lifecycle smoke covers a `posix_spawn_file_actions_addopen()`
      action before the listener child execs, extending pre-exec file-action
      coverage beyond dup/close operations.
- [x] The static lifecycle smoke covers a direct `vfork()` followed by
      `execl()` into a listener child, exercising parent resume and exec-time
      lease cleanup independently of the `posix_spawn*()` wrappers.
- [x] The static lifecycle smoke covers a direct `vfork()` followed by
      `execvp()` PATH lookup, extending the parent-resume and exec-time cleanup
      check to the libc PATH-search wrapper.
- [x] The static lifecycle smoke covers a direct `vfork()` followed by
      `fexecve()` from an `O_PATH` executable descriptor, exercising the
      descriptor-based `execveat` path.
- [x] The static lifecycle smoke covers a direct `vfork()` followed by
      `execvpe()` with an explicit child environment, extending PATH lookup
      and post-exec lease cleanup coverage beyond `execvp()`.
- [x] The static lifecycle smoke covers direct `vfork()` followed by
      `execve()` and `execle()`, extending post-exec cleanup coverage to
      explicit environment and variadic environment-passing entry points.
- [x] The static lifecycle smoke covers direct `vfork()` followed by
      `execv()`, extending vector-argument post-exec cleanup coverage beyond
      the variadic `execl()` path.
- [x] The static lifecycle smoke covers `daemon()` detaching the root leader
      before a child listener binds, validating lease tracking across the
      long-running service daemonization boundary.
- [x] The static lifecycle smoke launches a listener from the strict-shell
      wrapper and verifies that a shell descendant reserves, commits, and
      releases its Windows lease through the same traced process tree; a forced
      Windows bind rejection is propagated back through the shell command
      without committing the lease.
- [x] The static lifecycle smoke pauses and resumes a traced listener with
      `SIGSTOP`/`SIGCONT`, confirming that ordinary service stop/continue
      signals preserve the lease until the listener exits.
- [x] The static lifecycle smoke execs from a non-leader pthread and verifies
      that Linux thread-group identity reset preserves both inherited and
      replacement-image listener leases through final cleanup; duplicate
      owner-scoped teardown notifications remain bounded and idempotent.
- [x] Kernel-level coverage for the supported non-direct libc process creation
      matrix includes `system()`, `popen()`, `wordexp()`, `forkpty()`,
      `posix_spawn()`/`posix_spawnp()`, `POSIX_SPAWN_USEVFORK`, attributes, and
      individual plus combined pre-exec file actions. Arbitrary child-side work
      between `vfork()` and `exec`/`_exit` remains intentionally outside the
      contract, and setuid/setgid binaries remain rejected because ptrace cannot
      preserve their privilege semantics. The aarch64 register adapter remains
      buildable, but aarch64 runtime validation is intentionally outside this
      project's acceptance target.
- [x] Phase-one opt-in ptrace supervisor (`wsl-win-relay-run --kernel`) now
      coordinates direct single-process static amd64 TCP/UDP `bind/listen`
      syscalls through the existing lease protocol; process-tree ownership is
      now handled by the phase-two task/group model, while non-amd64 coverage
      remains follow-up work.
- [x] Phase-two process-tree ownership model is documented in ADR-0025:
      per-task fd state, ptrace fork/clone events, and owner-scoped
      `ADOPT`/`RELEASE` semantics define the implementation baseline for
      static child-process support.
- [x] Static process-style `fork()`/`clone(SIGCHLD)` children are now traced,
      inherit fd state, and adopt/release Windows lease ownership; direct
      vfork child operations are now traced, while unusual thread-group
      ownership remains a fail-closed follow-up boundary;
      non-thread `clone3()` and ordinary `CLONE_THREAD` are traced when
      supported.
- [x] Static descriptor cleanup through `fcntl(F_DUPFD*)` and
      `close_range()` is tracked with the same lease ownership rules;
      descriptor-table unsharing remains explicitly rejected in strict mode.
- [x] Leader-exit ownership migration excludes already-exiting thread tasks,
      re-evaluates stale owners during task removal, and avoids duplicate owner
      transfers during `PTRACE_EVENT_EXIT` ordering; the static smoke also
      covers synchronized multi-thread `SYS_exit` teardown.
- [x] Early ptrace stops for very short libc `vfork()` children are recovered
      when procfs confirms a tracked parent still has an unfinished create
      syscall and the stop is an unclassified initial `SIGSTOP`; unmatched or
      ambiguous stops remain fail-closed. Static `system()` and `popen()` smoke
      cases exercise the nested-vfork sequence.
- [x] Signal-terminated roots and thread-triggered `exit_group` teardown are
      covered by static supervisor smoke cases; lease cleanup remains
      single-shot when the group has no ordinary return path.
- [x] Kernel owner migration and cloned process groups compensate partial
      `ADOPT` failures with owner-scoped `RELEASE`, covered by a two-lease
      forced-failure supervisor smoke case.
- [x] `FD_CLOEXEC`, `dup3(O_CLOEXEC)`, and `close_range(..., CLOEXEC)` state is
      retired on `PTRACE_EVENT_EXEC`, so exec does not leave stale Windows
      listener reservations behind.
- [x] `socket(..., SOCK_CLOEXEC)` bindings enter the same exec-time retirement
      path and are covered by a static self-exec/rebind smoke test.
- [x] Process-style `clone(CLONE_FILES)` children share the supervisor group
      state and descriptor lease, matching Linux shared-fd semantics; an
      execing process-style child splits its descriptor state before
      `FD_CLOEXEC` retirement so the parent's leases remain intact.
- [x] Dynamic raw `clone()` and `clone3()` paths fail closed for non-thread
      `CLONE_FILES`; `clone3()` flags are read with `process_vm_readv` so an
      invalid caller pointer cannot crash the interposer.
- [x] Dynamic libc callback-style `clone()` rejects `CLONE_FILES` and
      `CLONE_VM|CLONE_VFORK` while tracked listeners exist; the interposer smoke
      covers both fail-closed paths before any child is created.
- [x] Dynamic `RESERVE`/`ADOPT` requests carry an optional Linux socket inode
      identity, allowing the control daemon to reclaim a lease when the
      descriptor disappears across successful `execve()` without requiring
      post-exec code in the replaced image; legacy requests remain accepted.
- [x] ptrace register access is isolated for amd64 and aarch64 in
      `native/strict_supervisor_regs.h`; unsupported architectures fail at
      compile time until a dedicated adapter is added.
- [x] The strict launcher now fails closed for directly executed static ELF
      and setuid/setgid targets instead of silently implying interposition;
      true kernel-level coverage remains a separate adapter boundary.
- [x] CI cross-builds the Linux proxy and Windows Go binaries for arm64;
      native arm64 ptrace/interposer runtime validation remains intentionally
      outside the acceptance target.
- [x] Opt-in broker-mode hot reconnect preserves existing TCP streams across
      connector/stdio replacement; a real delayed HTTP stream test covers the
      connector process boundary.
- [x] Broker-mode keeps an already-bound reverse listener usable through
      connector replacement; the integration test covers a new accepted stream
      after reconnect.
- [x] Broker-mode preserves an allowlisted reverse-UDP flow through connector
      replacement; the integration test verifies the response path.
- [x] Broker-mode preserves a procfs-discovered automatic WSL listener mapping
      through connector replacement; the integration test exercises the
      Windows-facing listener after reconnect.
- [x] Automatic TCP/UDP watcher mappings are generation-scoped, so an opener
      that completes after relay reset cannot republish a stale-session mapping.
- [x] Automatic mapping opens use a bounded worker pool so one slow Windows
      bind/relay request cannot serialize an entire listener scan.
- [x] The bounded mapping worker pool is covered by a concurrency regression
      test and keeps stale-session cleanup checks in every worker.
- [x] Relay reset and watcher shutdown cancel in-flight mapping attempts so a
      reconnect does not wait for the full per-port open timeout.
- [x] Initial session attachment does not reset freshly-created automatic
      mappings, and pending reverse listener OPEN/CLOSE races cancel before a
      Windows socket can be published.
- [x] Automatic mapping reset is driven directly by session clear callbacks,
      so rapid broker peer clear/set transitions cannot lose the reset event.
- [x] Strict lease rebind releases stale peer reservations before replacement
      binds, preserving leases while preventing stale Windows EADDRINUSE.
- [x] Strict leases whose replacement reservation is temporarily unavailable
      are retried in the background without disturbing already-restored
      reservations; rebind operations are serialized and covered by a focused
      unit test.
- [x] Automatic mapping refusals use bounded exponential retry backoff instead
      of issuing a Windows bind attempt on every procfs scan; session resets
      clear the backoff immediately.
- [x] Automatic mapping retry bounds are configurable through JSON and CLI
      settings with validation that preserves a non-decreasing backoff range;
      TCP and allowlisted UDP watchers share the same policy.
- [x] Logical relay object closes use bounded best-effort control-frame writes,
      so detached broker links cannot block shutdown while waiting for a future
      connector attachment.
- [x] Broker-mode can be selected from the persistent JSON configuration while
      attach credentials remain environment-only.
- [x] Optional systemd user broker unit, private token file, and bounded broker
      process restart wrapper are available; socket-owner crashes remain the
      documented established-socket failure boundary.
- [x] Broker instance identity detects a broker process restart and rebuilds
      stale peer state plus explicit/automatic mappings without restarting the
      WSL proxy; a real broker-restart integration test covers both directions.
- [x] Broker-mode becomes the proxy service default when the broker installer
      provisions `WSL_WIN_RELAY_BROKER_MODE=1` in the private environment; JSON
      and command-line configuration remain available for explicit control.
- [x] Broker installation records and validates the Windows connector path;
      the proxy service wrapper overrides `relay_exe` in broker mode so a new
      installation cannot accidentally launch the stdio relay from stale JSON.
- [x] Broker installer upgrades migrate missing legacy executable fields and
      reject broker/connector path conflicts instead of silently retaining a
      different active binary.
- [x] Broker installation verifies matching broker/connector version, commit,
      and build-time metadata before service restart, with an explicit legacy
      bypass rather than an implicit mixed-binary deployment.
- [x] Broker installation restarts an already-installed proxy user unit after
      broker startup, while still allowing either unit to be installed first.
- [x] Process-isolated broker frontend/bridge-worker/socket-host/socket-owner
      design is documented in ADR-0016; only the socket owner owns relay state.
- [x] Frontend, bridge-worker, and socket-host bridge crash recovery preserves
      established socket-owner-owned sockets and automatic/explicit/strict
      mappings; the integration test covers live delayed streams across all
      outer bridge force-kills.
- [x] Broker systemd unit uses `KillMode=process` so frontend restart does not
      terminate the bridge worker, socket-host bridge, or socket owner; normal stop uses cascading
      private control endpoints.
- [x] Frontend and bridge-worker supervisors continuously reap children they
      start and keep externally-owned roles reusable without claiming their
      lifecycle.
- [x] Socket-owner health probes detect a dead reused owner and rebuild the
      owner plus automatic/explicit/strict mappings; established streams end at
      the socket-owner crash boundary.
- [x] Broker connector credentials are propagated through a normalized,
      flag-free `WSLENV` entry; the contract is documented in ADR-0017.
- [x] Private worker/socket-host health probes are token-bound and reject stale
      same-endpoint processes owned by a different broker identity.
- [x] A confirmed token-mismatched role is stopped and drained before endpoint
      reuse, while transient probe failures leave externally-owned roles alone.
- [x] Windows broker interop smoke can route through a Windows-side upstream
      proxy without requiring WSL to reach that proxy endpoint.
- [x] Broker installation validates the private token file as a regular,
      protected hexadecimal credential and rejects environment/token mismatches
      before service restart.
- [x] Socket-host bridge crash recovery preserves already-established streams
      because the socket owner, rather than the bridge, owns the kernel sockets.
- [x] Socket-owner crash recovery rebuilds the owner, outer bridges, and new
      mappings; established streams remain the documented unrecoverable boundary.
- [x] Broker executable provides a host-level `-supervise` parent that restarts
      a crashed frontend with bounded backoff; the broker user-service wrapper
      uses it by default while preserving status-2 configuration failures.
- [x] Broker supervisor and internal roles accept a protected `-token-file` and
      propagate its path instead of exposing token contents in child arguments;
      legacy environment and `-token-hex` paths remain supported.
- [x] Broker connector retries transient local-IPC endpoint and handshake
      outages for a bounded window so WSL proxy services survive supervised
      frontend replacement; authentication/configuration failures remain
      terminal.
- [x] Broker attach handshakes have a bounded per-connection deadline and clear
      it after authentication, so stalled local IPC clients cannot leak broker
      goroutines or leave phantom registry generations; connectors apply the
      same deadline while waiting for a broker response.
- [x] Optional Windows Task Scheduler installation provides a host-level
      `-supervise` boundary that can keep the broker available across WSL VM or
      user-service shutdown; established streams after a socket-owner crash
      remain intentionally unrecoverable.
- [x] New systemd broker installations create a protected `attach.token` and
      pass its converted Windows path to the Windows broker while retaining
      legacy environment token compatibility for existing deployments.
- [x] Broker mode rejects a WSL-side upstream proxy setting early because the
      connector does not own outbound dialing; broker upstream configuration is
      required on the Windows broker/service boundary.

The hot-reconnect implementation follows [ADR-0015](../adr/0015-persistent-windows-ownership-and-attach.md) and [ADR-0016](../adr/0016-process-isolated-socket-owner.md): relay state and socket ownership now live in an independent socket owner behind replaceable connector bridges.

---

### Task 1: Establish repository and design contracts

**Files:** `README.md`, `docs/adr/0001-layered-relay-architecture.md`, `docs/plans/2026-09-12-wsl-win-relay.md`

Write the supported first release, non-goals, security assumptions, process lifecycle, and protocol layering. Record why process pipes are the initial transport and why shared memory/TUN are deferred adapters rather than foundations.

Verify that the documents describe a bounded frame size, explicit stream lifecycle, and no-auth loopback-only SOCKS5 default.

### Task 2: Implement the framed protocol

**Files:** `internal/protocol/frame.go`, `internal/protocol/frame_test.go`

Implement fixed header encoding/decoding with magic, protocol version, frame type, stream ID, and payload length. Reject malformed headers, unknown types, oversized payloads, and truncated frames. Keep writes atomic through a caller-provided serialized writer.

Run `go test ./internal/protocol -v`.

### Task 3: Implement stdio transport

**Files:** `internal/transport/stdio/stdio.go`, `internal/transport/stdio/stdio_test.go`

Provide a small `io.ReadWriter`/`io.Closer` wrapper for process stdin/stdout and in-memory tests. Keep stderr outside the binary data path.

Run `go test ./internal/transport/stdio -v`.

### Task 4: Implement multiplexed relay client and server

**Files:** `internal/relay/client.go`, `internal/relay/server.go`, `internal/relay/relay_test.go`

Implement stream open/data/close/error semantics, bounded per-stream buffering, serialized frame writes, cancellation, and clean shutdown. Expose a `net.Conn`-compatible client stream and a server dialer interface. Add an in-memory end-to-end test with a local TCP echo service.

Run `go test ./internal/relay -race -v`.

### Task 5: Implement SOCKS5 adapter

**Files:** `internal/socks5/server.go`, `internal/socks5/server_test.go`

Implement SOCKS5 no-auth negotiation and CONNECT for IPv4, IPv6, and domain targets. Use the relay client as a dialer, map connection errors to protocol replies, and proxy bytes in both directions. Bind only to loopback by default.

Run `go test ./internal/socks5 -race -v`.

### Task 6: Add production entrypoints and operator documentation

**Files:** `cmd/wsl-proxy/main.go`, `cmd/win-relay/main.go`, `README.md`, `docs/adr/0002-socks5-first-adapter.md`

Add `wsl-proxy` flags for listener address and Windows relay executable, process signal handling, stderr logging, and `win-relay` server startup. Document build commands, WSL usage, environment variables, diagnostics, and security restrictions.

Run `go test ./...`, build Linux and Windows binaries, and run a local Windows-side relay integration check where available.

### Task 7: Cross-environment verification

Clone the repository in WSL, build the Linux proxy and Windows relay, exercise `curl` through SOCKS5, and record any interop or path quoting issues. Keep this verification separate from protocol unit tests so WSL/HNS failures are distinguishable from application failures.

### Task 8: Reverse port forwarding

Extend the protocol with listener registration and inbound stream frames. Add a Windows listener manager and a WSL mapping that dials a configured local destination for each accepted connection. Return Windows bind failures to the WSL process and preserve half-close semantics for forwarded connections. Keep automatic discovery of arbitrary application listeners out of this task; it requires a separate transparent interception adapter.

### Task 9: Automatic WSL listener discovery

Poll `/proc/net/tcp` and `/proc/net/tcp6`, normalize dual-stack listeners, and dynamically reconcile Windows reverse forwards. Provide safe loopback defaults, include/exclude policies, cleanup, retry after Windows rejection, parser tests, and a real WSL-to-Windows lifecycle check.

### Task 10: Strict synchronized listen mode

Design an opt-in launcher/interposition path that registers the Windows listener before the application observes `listen(2)` success. Return the Windows bind error to the application when registration fails. Document unsupported static/setuid binaries and keep polling discovery as the compatibility fallback.

### Task 11: UDP relay and SOCKS5 UDP ASSOCIATE

Add endpoint-preserving datagram frames and Windows UDP socket lifecycle management. Then implement SOCKS5 UDP ASSOCIATE, including domain destinations, source endpoint responses, association ownership, timeouts, and malformed or fragmented packet rejection.

### Task 12: Transparent WSL routing

Integrate a pinned tun2socks release as an external TUN adapter. Provide route/DNS/device setup with complete rollback, preserve an uplink route for the local relay endpoint, document root and `/dev/net/tun` requirements, and verify a proxy-unaware TCP/UDP application through Windows egress.

### Task 13: Long-running service integration

Provide a systemd user unit and idempotent installer. Restart the proxy after child-process failure, preserve private configuration permissions, and verify that startup handshake and explicit mappings are recreated after a restart while strict listener leases are rebound when the control socket remains alive.
