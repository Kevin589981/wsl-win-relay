# wsl-win-relay

An emergency WSL-to-Windows network relay for cases where WSL networking is broken but Windows still has connectivity.

The first release exposes a loopback SOCKS5 proxy inside WSL. A Windows helper process performs outbound TCP and UDP connections, and the two processes exchange multiplexed frames over stdin/stdout. The design keeps protocol, transport, relay, and user-facing adapters independent so the optional transparent adapter does not become a protocol dependency.

## Status

The repository is under active implementation. The current TCP/UDP relay
milestone is usable and tested:

- Versioned, bounded multiplexed protocol with explicit stream lifecycle.
- Stdio transport for WSL-to-Windows process interop.
- Windows-side WinSock TCP dialing, including Windows-side DNS for domain targets.
- Loopback SOCKS5 no-auth proxy with IPv4, IPv6, and domain CONNECT.
- TCP half-close propagation so TLS/HTTP clients can finish writes before reading responses.
- Cross-platform builds and WSL interop integration coverage.
- Dynamic `/proc/net/tcp{,6}` listener discovery with automatic Windows add/remove.
- Strict opt-in `listen()` coordination for dynamically linked Linux applications.
- Explicit reverse UDP forwarding with per-source flow isolation.
- Multiplexed Windows-side UDP sockets with endpoint-preserving datagram frames.
- SOCKS5 UDP ASSOCIATE for DNS, QUIC-capable clients, and other UDP traffic.
- Optional HTTP CONNECT proxy for tools that only support `HTTP_PROXY`.
- Optional Windows-side HTTP CONNECT or SOCKS5/SOCKS5H upstream proxy; SOCKS5
  upstreams also carry relay UDP via UDP ASSOCIATE.
- Per-stream 256 KiB credit windows that isolate slow TCP consumers.
- Startup capability negotiation before any proxy or mapped port is advertised.
- Idempotent systemd user-service installation with private configuration permissions and restart-on-relay-failure.
- Broker installation validates the protected token file and rejects missing,
  malformed, or environment-mismatched credentials before restarting services.
- Control-socket startup is exclusive: an active prior instance is preserved and
  rejected, while an unreferenced stale socket is cleaned up safely.
- Verified in the target failure mode: WSL could not reach the configured Windows proxy port, while this relay still reached the public Internet and cloned a GitHub repository.

Explicit reverse port forwarding and strict synchronization with dynamically linked application `listen()` calls are implemented. The broader automatic mode remains polling-based so it can support unmodified applications.

Automatic discovery is available as an opt-in polling mode. It mirrors detected TCP listeners after they begin listening. This provides zero-configuration reachability but cannot retroactively make the application's already-successful `listen(2)` fail when Windows rejects the corresponding port; use the strict launcher when rejection propagation is required.

The persistent-broker foundation is now staged in `internal/transport/attach`:
it provides a per-instance token, generation-safe ownership, a bounded versioned
attach handshake, and deterministic registry-summary/resume-ack messages. The
transport-independent broker core in `internal/broker` now accepts those
sessions and tracks stable entry IDs. The Windows broker/connector that owns
sockets across connector restarts is enabled by the broker user-service
installer; manually launched proxies remain stdio by default. A broken stdio
session still ends in-flight connections while new requests and mappings
recover normally.

An opt-in broker transport is available for integration testing. Build with
`scripts/build-wsl.sh`, start `wsl-win-broker.exe` on Windows with a private
`WSL_WIN_RELAY_ATTACH_TOKEN` and endpoint, then set the same token and
`WSL_WIN_RELAY_BROKER_ENDPOINT` in WSL and configure `relay_exe` as
`wsl-win-connector.exe`. The connector performs attach/resume before forwarding
the existing relay byte stream. The broker now keeps one relay server alive and
swaps the attached connector transport, so a connector break no longer tears
down broker-side sockets immediately. In `-broker-mode`, WSL also keeps one
relay client and stream registry, rehandshakes the replacement connector, and
preserves in-flight TCP streams. `scripts/test-broker-reconnect.sh` verifies
this with a delayed HTTP response and a broker-owned reverse listener that
accepts a new stream after replacement. The same test covers a reverse-UDP
echo flow after replacement. The broker executable now uses a replaceable
frontend, bridge worker, socket-host bridge, and durable socket owner.
Frontend, bridge-worker, or socket-host bridge crashes leave socket-owner-owned
sockets and established streams alive; a socket-owner crash still ends them.
When any outer bridge restarts, it reuses the same socket owner and the WSL
proxy rebuilds any registrations that need it.

`scripts/test-broker-auto-rebind.sh` separately verifies that a procfs-discovered
WSL listener remains reachable through its automatically created Windows port
after the connector is replaced. `scripts/test-broker-restart.sh` also verifies
that a broker process restart is detected and both automatic and explicit
Windows mappings are reconstructed by the still-running WSL proxy.

Set `"broker_mode": true` in the JSON configuration to persist this mode for
the systemd user service. When `install-broker-user-service.sh` creates the
private broker environment it also sets `WSL_WIN_RELAY_BROKER_MODE=1`, which
makes broker mode the proxy service default once that environment is loaded.
Keep `WSL_WIN_RELAY_ATTACH_TOKEN` and
`WSL_WIN_RELAY_BROKER_ENDPOINT` in the service environment; the token is
intentionally not accepted from the configuration file.

For systemd-managed broker startup, set `WSL_WIN_RELAY_BROKER_EXE` to the
mounted Windows `wsl-win-broker.exe` path and run
`./scripts/install-broker-user-service.sh`. It creates a mode-0600
`broker.env`, generates the attach token once, and enables
`wsl-win-relay-broker.service` with a bounded restart policy. The broker
service wrapper starts the broker's host-level `-supervise` parent, which keeps
the public frontend recoverable after an abnormal exit. The broker executable
keeps socket ownership in a separate socket-owner child behind a
socket-host bridge, so frontend, bridge-worker, and socket-host bridge crashes
do not close established kernel sockets; a socket-owner crash still does. The normal
proxy service wrapper loads the same env file for connector children and
selects broker mode when `WSL_WIN_RELAY_BROKER_MODE=1`. After a bridge-worker
restart, the running proxy reconnects through the same socket owner and
reconstructs explicit and automatic mappings when needed. The bridge and owner
roles have separate token-bound health probes, so stale role processes are
drained before endpoint reuse.

New broker installations also create `attach.token` with mode `0600` and pass
that path to the Windows broker (the wrapper converts a WSL path with
`wslpath -w` before invoking a Windows `.exe`). The private environment still
contains the token value for WSL connector propagation through `WSLENV`; older installations
without `WSL_WIN_RELAY_ATTACH_TOKEN_FILE` continue to use the legacy
`-token-hex` fallback until migrated.
The broker unit uses `KillMode=process` so systemd frontend restarts do not
terminate the bridge worker, socket-host bridge, or socket owner; a normal stop still shuts them down
through the private control endpoints.

If the broker must outlive the WSL VM or user service, install the optional
Windows Task Scheduler boundary from PowerShell 7:

```powershell
.\scripts\install-broker-windows-task.ps1 `
  -BrokerExe 'C:\Tools\wsl-win-broker.exe' `
  -TokenFile 'C:\Users\you\.config\wsl-win-relay\attach.token' `
  -StartNow
```

The installer protects the token file ACL and registers the host-level
`-supervise` parent. The WSL proxy still uses the same token value through its
private environment/`WSLENV`; the task only receives the token-file path. Use
`-Uninstall` with the same `-TaskName` to remove the task. This boundary keeps
future attachments available across WSL shutdown, but cannot preserve
established streams after a socket-owner crash.

To verify the complete WSL-to-Windows broker path on a machine with WSL
interop and working Windows egress, run:

```bash
./scripts/test-broker-windows-interop.sh
```

The smoke builds the Linux proxy and Windows broker/connector, starts the
broker on a per-user named pipe, and performs a SOCKS5 request to
`https://example.com`. WSL does not automatically export arbitrary environment
variables to Windows processes, so broker mode adds
`WSL_WIN_RELAY_BROKER_ENDPOINT` and `WSL_WIN_RELAY_ATTACH_TOKEN` to `WSLENV`
for connector children. The token remains out of command-line arguments. See
[ADR-0017](docs/adr/0017-wslenv-credential-propagation.md) for the normalization
rule that preserves unrelated entries while forcing these two names to be
single, flag-free entries.

The Windows broker also accepts `-token-file` for host-service deployments.
The file must be a private regular file containing the hexadecimal token; on
Unix it must not be group/world accessible. The supervisor and its internal
roles pass this path instead of the token contents, while `-token-hex` and the
environment variable remain supported for compatibility.

The broker connector keeps its stdio service alive across the bounded endpoint
outage created by a supervised frontend replacement. It retries transport-level
dial and handshake failures for up to 30 seconds with capped backoff, while a
rejected token or invalid connector configuration fails immediately.

To exercise the same path while keeping an upstream proxy on the Windows side,
set `WWR_WINDOWS_UPSTREAM_PROXY` when running the smoke. WSL still sends only
the target through the relay; the Windows broker performs the upstream
connection:

```bash
WWR_WINDOWS_UPSTREAM_PROXY=socks5h://matebookxpro.local:7890 \
  ./scripts/test-broker-windows-interop.sh
```

In broker mode, configure the upstream proxy on `wsl-win-broker.exe` (or its
private service environment), not on `wsl-proxy`. The WSL connector only carries
the attach stream; `wsl-proxy -broker-mode -upstream-proxy ...` is rejected at
startup to avoid passing an unsupported flag to the connector.

## Security model

- The WSL listener binds to `127.0.0.1` by default.
- The stdio relay reads commands only from its parent process pipes. Broker
  frontend, bridge-worker, socket-host bridge, socket-owner, and control endpoints use same-user local IPC with no LAN
  listener.
- The WSL-facing SOCKS5 and HTTP listeners have no client authentication; do
  not bind them to a LAN address. Upstream proxy credentials, when configured,
  are used only for the Windows-side upstream connection.
- The relay is intended for the same user's WSL and Windows processes, not as a general network service.

## Layered adapters

```text
SOCKS5 / HTTP CONNECT / TUN transparent adapter
              |
       multiplexed relay
              |
   stdio / future transports
              |
      Windows WinSock
```

The SOCKS5 and HTTP CONNECT adapters are suitable for proxy-aware command-line
tools. The TUN adapter is implemented as an opt-in operational layer and needs
root, `/dev/net/tun`, `iproute2`, and the pinned `tun2socks` binary; it remains
separate from the relay core so it can be replaced without changing stream or
datagram semantics.

See [the implementation plan](docs/plans/2026-09-12-wsl-win-relay.md) and [architecture ADR](docs/adr/0001-layered-relay-architecture.md).

## Port direction semantics

Outbound proxy connections do **not** need matching ports. For a request such as `curl -> example.com:443`, WSL only sends the destination; Windows creates an ordinary outbound socket and chooses an ephemeral source port. Source-port correspondence would add no useful information and would create avoidable collisions.

Inbound exposure is different. A Windows port must be bound before Windows clients can connect. The reverse-forward mapping looks like `windows-port:WSL-address`, for example `8000:127.0.0.1:8000`; the WSL application keeps owning its local `8000`, while the relay owns Windows `8000` and connects to the WSL application for each accepted connection.

## Build

Build the Linux proxy and Windows relay from the repository root. From Windows PowerShell (use `arm64` on an ARM64 WSL/Windows pair):

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; go build -o bin/wsl-proxy-linux ./cmd/wsl-proxy
$env:GOOS='windows'; $env:GOARCH='amd64'; go build -o bin/wsl-win-relay.exe ./cmd/win-relay
Remove-Item Env:GOOS,Env:GOARCH
```

`scripts/build-wsl.sh` selects `amd64` or `arm64` from `uname -m`; set
`WSL_WIN_RELAY_GOARCH=amd64|arm64` to override it for Go cross-builds. Set
`WSL_WIN_RELAY_OUTPUT_DIR` to place a build in a separate staging directory.
The native strict supervisor is compiled for the running WSL architecture, so
an arm64 cross-build of the Go binaries is not a substitute for native ptrace
runtime validation. Native arm64 runtime validation is outside the current
verification target.
`wsl-proxy-linux` is the binary to run inside WSL; `wsl-win-relay.exe` is
launched by it through WSL interop. Alternatively, run `go build` for the
Linux proxy directly inside WSL.

## Configuration

For long-running use, start from [`wsl-win-relay.example.json`](wsl-win-relay.example.json):

```bash
./bin/wsl-proxy-linux -config ./wsl-win-relay.json
```

The JSON decoder rejects unknown fields so misspelled safety or bind settings do
not silently disappear. Command-line options override scalar configuration
values; repeated command-line `-reverse` mappings are added to configured
mappings. SOCKS5 UDP associations are reclaimed after
`udp_associate_idle_timeout` (default `5m`) without traffic; override it with
`-udp-associate-idle-timeout` when needed.
Relay connection setup waits at most `relay_dial_timeout` (default `30s`) for a
healthy Windows session; override it with `-relay-dial-timeout` when a longer
recovery window is required.
The startup capability handshake has its own `relay_handshake_timeout`
(default `5s`), configurable with `-relay-handshake-timeout` when launching the
Windows child is slow after recovery.

Set `upstream_proxy` or `-upstream-proxy` when Windows itself should use an
upstream proxy, for example `socks5h://matebookxpro.local:7890`. The default
is direct Windows WinSock egress. SOCKS5/SOCKS5H upstreams proxy both relay
TCP streams and relay UDP datagrams through UDP ASSOCIATE. HTTP/HTTPS upstreams
only support TCP CONNECT; relay UDP remains native Windows UDP because HTTP
CONNECT has no interoperable UDP datagram mode.

Place `wsl-win-relay.exe` somewhere visible to WSL interop (or pass its absolute path with `-relay-exe`) and start:

```bash
./bin/wsl-proxy -relay-exe /mnt/c/Users/<user>/bin/wsl-win-relay.exe
curl --proxy socks5h://127.0.0.1:1080 https://example.com
```

The `socks5h` form is intentional when the upstream should resolve names
remotely: TCP hostnames are sent through the relay rather than resolved by WSL.
The `socks5` form resolves TCP and UDP names on Windows before sending IP
addresses to the upstream. SOCKS5 UDP ASSOCIATE is supported; SOCKS5H UDP
destinations can remain domain names for upstream resolution, while native UDP
uses Windows resolution. SOCKS fragmentation
(`FRAG != 0`) is rejected because there is no interoperable fragmentation
standard in common clients.

For clients that only support an HTTP proxy, enable the optional CONNECT listener:

```bash
./bin/wsl-proxy-linux \
  -relay-exe /mnt/d/Code/net/wsl-win-relay/bin/wsl-win-relay.exe \
  -http-listen 127.0.0.1:8080

HTTPS_PROXY=http://127.0.0.1:8080 curl https://example.com
```

Only CONNECT is accepted. Plain HTTP forwarding is deliberately not implemented;
clients using `HTTP_PROXY` for cleartext URLs should use SOCKS or request CONNECT.

To expose a WSL service on a Windows port, add an explicit reverse mapping:

```bash
./bin/wsl-proxy-linux \
  -relay-exe /mnt/d/Code/net/wsl-win-relay/bin/wsl-win-relay.exe \
  -reverse 0.0.0.0:8000=127.0.0.1:8000 \
  -reverse 127.0.0.1:9000=127.0.0.1:9000
```

The WSL application continues to bind `127.0.0.1:8000`; the Windows relay
owns `0.0.0.0:8000` and forwards each accepted connection. A Windows bind
conflict is reported during startup. All repeated `-reverse` registrations are
transactional: if one fails, earlier registrations are removed. Firewall policy
can still reject later connections, so it must be checked separately.

For a WSL UDP service, use a separate explicit UDP mapping:

```bash
./bin/wsl-proxy-linux \
  -relay-exe /mnt/c/Users/<user>/bin/wsl-win-relay.exe \
  -reverse-udp 127.0.0.1:5353=127.0.0.1:5353
```

The Windows relay binds the UDP port and forwards each source endpoint to the
WSL target through an isolated local flow. Responses return to the original
Windows source. A bind conflict is reported while the mapping starts. Each
mapping caps active source flows at 1024; idle flows are reclaimed after five
minutes and new sources are dropped while the cap is reached.
Unrestricted automatic listener discovery remains TCP-only because
`/proc/net/udp` cannot safely distinguish a UDP server socket from an
ephemeral client socket; the allowlisted UDP mode below is deliberately
conservative and opt-in.

An explicitly allowlisted UDP discovery mode is also available for common
unconnected UDP services:

```bash
./bin/wsl-proxy-linux \
  -relay-exe /mnt/d/Code/net/wsl-win-relay/bin/wsl-win-relay.exe \
  -auto-forward \
  -auto-forward-udp \
  -auto-forward-udp-include 5353,8125
```

This mode scans `/proc/net/udp{,6}` and mirrors only the listed non-zero ports
whose socket has no connected remote endpoint. Linux procfs does not identify
UDP server sockets, so a client that happens to bind one of the allowlisted
ports can still be observed; the mandatory allowlist keeps that ambiguity
bounded. Use strict launcher UDP `bind()` coordination when Windows rejection
must be returned to the application before `bind()` succeeds.

To discover WSL listeners dynamically and bind matching Windows loopback ports:

```bash
./bin/wsl-proxy-linux \
  -relay-exe /mnt/d/Code/net/wsl-win-relay/bin/wsl-win-relay.exe \
  -auto-forward
```

Useful controls:

```text
-auto-forward-host 127.0.0.1       Windows bind host; use 0.0.0.0 deliberately for LAN access
-auto-forward-host6 ::1             Windows IPv6 bind host; use :: deliberately for LAN access
-auto-forward-include 8000,9000    Optional allowlist; empty means all discovered ports
-auto-forward-exclude 22,53        Ports that must never be mirrored
-auto-forward-interval 1s          Discovery interval
-auto-forward-retry-min 1s         Minimum delay after a Windows refusal
-auto-forward-retry-max 30s        Maximum delay after repeated refusals
```

The SOCKS5 listener and explicit reverse-forward destinations are excluded automatically. Automatic mappings are removed when their WSL listener disappears. The watcher lives for the whole proxy process: when the Windows relay child is replaced, old mappings are closed and recreated on the replacement session after it becomes ready.
Each automatic mapping attempt is bounded by `relay_dial_timeout`; a relay
outage therefore cannot block listener discovery indefinitely, and the next
scan retries it after the session recovers.
When Windows rejects a discovered port, repeated attempts use a bounded
exponential backoff (one second initially, capped at thirty seconds) instead
of hammering the relay on every scan. A relay-session reset or disappearance of
the WSL listener clears that backoff. The retry bounds are configurable with
the two flags above or the `auto_forward.retry_min` and
`auto_forward.retry_max` JSON fields; the maximum must be greater than or equal
to the minimum.

To run the real WSL/Windows recovery check after building both binaries, use
`./scripts/test-auto-rebind.sh`. It requires WSL Windows interop and verifies
that an allowlisted Windows mapping becomes reachable again after the relay
child is terminated. Set `WWR_WINDOWS_SHELL` to an absolute mounted path when
the shell is not in `PATH`, for example
`WWR_WINDOWS_SHELL=/mnt/d/AppGallery/Downloads/PowerShell/7/pwsh.exe`.

IPv4 and IPv6 Windows bind hosts are configured independently. The defaults are
`127.0.0.1` and `::1`; set `-strict-listen-host6` and/or `-auto-forward-host6`
when the Windows-facing IPv6 bind should use another address.

## Strict synchronized listen

Build the WSL launcher and interposer with GCC:

```bash
./scripts/build-wsl.sh
```

Keep `wsl-proxy-linux` running, then launch an application through the wrapper:

```bash
./scripts/wsl-win-relay-run python3 -m http.server 8000
```

The wrapper waits up to two seconds for the strict-listen control socket and
fails early with a diagnostic if the relay service is not running.

It also rejects directly executed static ELF and setuid/setgid targets before
launch. Those targets cannot load `LD_PRELOAD`, so allowing them through would
silently disable the Windows-before-WSL bind contract. Scripts and other
non-ELF entrypoints remain allowed; remaining static-binary coverage is
provided by a broader kernel-aware lifecycle adapter. An opt-in ptrace adapter is now available
for Linux amd64 targets, including process-style `fork()` children:

```bash
./scripts/wsl-win-relay-run --kernel ./static-service 8000
```

It coordinates direct TCP/UDP `bind()` and TCP `listen()` syscalls through the
same control socket. Process-style `fork()`, `clone(SIGCHLD)`, non-thread
`clone3()`, and ordinary `CLONE_THREAD` pthreads are attached with task/group
state and inherit lease ownership with `ADOPT`/`RELEASE`. The kernel adapter is
deliberately opt-in and traces `vfork()` children through the same process
ownership path; unusual thread-group teardown remains unsupported. The source
includes an aarch64 ptrace register adapter, but aarch64 is outside the current
verification target. Setuid/setgid targets are rejected in both launcher modes
because ptrace cannot preserve their privilege semantics.
Use the default interposer for dynamically linked applications.

Before the application's libc `listen()` succeeds, the wrapper reserves
Windows `127.0.0.1:8000` for IPv4 or `[::1]:8000` for IPv6. If Windows reports that the address is already in
use, the application receives Linux `EADDRINUSE` and its `listen()` fails. If
Linux itself rejects the listen, the Windows reservation is aborted. Windows
does not accept clients until both sides have succeeded.

The same coordination applies to non-zero UDP `bind()` calls. A WSL UDP
service can therefore be exposed on the same Windows port without a manual
`-reverse-udp` entry. `bind(...:0)` remains native-only so ordinary ephemeral
UDP clients are not mirrored.

If the control socket is briefly unavailable while the relay is starting or
recovering, or a request times out while the relay session is being replaced,
the interposer retries the reservation for up to two seconds.
The corresponding `COMMIT` and `ADOPT` operations use the same transient retry
policy; cleanup operations remain best-effort and single-shot during shutdown.
Definitive Windows bind errors are returned immediately. Set
`WSL_WIN_RELAY_CONTROL_RETRY_SECONDS` to extend this window (up to 60 seconds)
when the relay supervisor uses a longer restart backoff; the strict launcher
uses the same value while waiting for the control socket.

The relay refuses to replace an active control socket from another instance.
Only a socket that no longer has a listener is removed during startup, which
prevents two supervisors from silently publishing different reservation state.

This propagates bind/listen errors, not later firewall policy. A Windows
firewall rule that drops or rejects clients after the socket is bound does not
make the Windows `bind()` fail, so it cannot be reflected in the original WSL
`listen()` call.

Set `WSL_WIN_RELAY_DEBUG=1` to print control requests and responses from the
interposer. `WSL_WIN_RELAY_CONTROL` and `WSL_WIN_RELAY_PRELOAD` override the
default control socket and shared-library paths.

Strict mode currently covers dynamically linked applications using libc or
direct `syscall(SYS_listen/SYS_bind)` calls, including TCP `listen()`, non-zero
UDP `bind()`, `dup()`, `dup2()`, `dup3()`, `fcntl(F_DUPFD*)`, `close_range()`,
ordinary `fork()` descriptor inheritance, process-style `clone()` without
`CLONE_FILES`, and parent-side `vfork()` adoption. The kernel adapter additionally
tracks `SOCK_CLOEXEC`/`FD_CLOEXEC` through `PTRACE_EVENT_EXEC`; the dynamic
interposer cannot run post-exec cleanup in the replaced image, so it includes
the socket inode identity in `RESERVE`/`ADOPT` and lets the control daemon's
reaper reclaim leases whose descriptor disappeared at that boundary. The
dynamic interposer rejects process-style
`CLONE_FILES` in the raw/libc `clone()` paths because its tracking table is
process-local. It applies the same check to `clone3()` by safely reading the
caller's flags; malformed or unreadable clone arguments fail closed with
`ENOTSUP`. Static or shared-fd process creation should use the opt-in kernel
adapter.
Static binaries should use the
opt-in `--kernel` adapter; setuid binaries remain rejected. Child-side
networking before `vfork()` `exec`/`_exit` is supported only for direct
syscall-safe operations. Ordinary pthread/`CLONE_THREAD`
listeners share the process lease by design and are covered; unusual
thread-group teardown and signal/exec interactions remain outside the strict
adapter contract. `close_range(CLOSE_RANGE_UNSHARE)` is rejected rather than
silently weakening descriptor ownership guarantees.
The kernel supervisor also recovers the initial unclassified child stop seen
with nested libc `vfork()` launches when the parent relationship and pending
create syscall are both unambiguous; static `system()` and `popen()` smoke
cases cover this path, while ambiguous variants remain fail-closed.
The native lifecycle smoke also exercises `posix_spawnp()` PATH lookup and
direct `vfork()` followed by `execl()` or `execvp()`; these paths retain the
same bounded ownership and cleanup guarantees. The kernel adapter does not
promise arbitrary child-side work between `vfork()` and `exec`/`_exit`.
The static lifecycle smoke also covers `daemon()` detaching the root leader
before a child listener binds, which is a supported process-tree boundary for
long-running services.
The native interposer targets the Linux
amd64 build produced by the WSL scripts. The daemon tracks multiple process
owners and reaps leases from processes that exit without closing their
descriptors.

The native lifecycle regression test can be run offline with
`./scripts/test-interposer.sh`; it uses a local fake control socket and does
not open a Windows port. It covers both the successful TCP/UDP lifecycle and a
simulated Windows `EADDRINUSE` response that must make the WSL `listen()` fail.

## Transparent mode

For applications without proxy support, install the pinned TUN adapter and keep
the relay running:

```bash
./scripts/install-tun2socks.sh
sudo env WWR_TUN_PROXY=socks5://127.0.0.1:1080 ./scripts/transparent-relay.sh
```

The script creates `tun0`, adds split default routes, keeps a configured
non-loopback proxy endpoint on the original uplink, starts tun2socks, and
restores routes and the optional DNS file on exit. Signal handling includes a
bounded tun2socks shutdown with a forced-kill fallback, so a stuck adapter
cannot leave route cleanup waiting forever. Set `WWR_DNS=1.1.1.1` when
WSL DNS is unavailable; set `WWR_UPLINK_INTERFACE` if the default interface
cannot be detected. Root, `iproute2`, `/dev/net/tun`, and tun2socks are required.
The script searches `PATH` and the Go `GOPATH/bin` installation location; set
`WWR_TUN2SOCKS_BIN` when running under `sudo` or another environment with a
different tool path.

When WSL has lost its default interface because of an HNS failure, the script
automatically falls back to `lo` if `WWR_TUN_PROXY` points at a local loopback
SOCKS endpoint. Set `WWR_UPLINK_INTERFACE` explicitly for a non-loopback proxy.

The TUN setup and rollback path has been smoke-tested under WSL as root,
including IPv4 split routes, optional IPv6 routes, process shutdown, and device
cleanup. DNS restoration also preserves the original `/etc/resolv.conf` shape,
including a dangling symlink when that is what WSL provided.
Set `WWR_RESOLV_CONF` to override the DNS file path in a container or test
namespace; it defaults to `/etc/resolv.conf`.

A real transparent TCP/UDP smoke test has passed with the pinned `tun2socks`
v2.7.0 binary: in a WSL instance with no `eth0` or default route, setting
`WWR_UPLINK_INTERFACE=lo` and `WWR_DNS=1.1.1.1` sent an environment-clean
`curl https://example.com` through TUN, the local SOCKS5 relay, and the Windows
relay. The test also verified that the TUN device, split routes, relay process,
and DNS state were cleaned up afterward.

The same test also passed with the Windows relay configured for
`socks5h://matebookxpro.local:7890`, demonstrating the intended failure-mode
path: WSL only reaches its local relay, while Windows resolves and connects to
the upstream proxy.

## Long-running service

With WSL systemd enabled, install the user service:

```bash
./scripts/install-user-service.sh
systemctl --user status wsl-win-relay.service
```

Re-running the installer updates the installed binaries and unit, then
restarts the user service so the new configuration is active immediately.

The service restarts the proxy after a Windows relay crash or broken stdio
transport; startup handshake and reverse registrations are recreated on each
restart. With `broker_mode` enabled, connector, broker-frontend, or bridge-worker
restarts preserve socket-owner-owned TCP/UDP sockets. A socket-owner process
crash still loses those sockets. The strict control socket remains
available across relay sessions,
and live leases are rebound and recommitted when the replacement child is
ready. If a replacement Windows bind is temporarily refused, missing strict
leases are retried in the background without disturbing leases that already
recovered. The proxy also retries a relay-only EOF on its own
with an exponential backoff from two seconds up to thirty seconds when run
directly; after a minute of stable operation the next failure starts again at
two seconds. Local SOCKS5/HTTP listener ports stay bound while a replacement
session starts, and new requests wait for it; in-flight streams still end with
the failed session. A relay child that exits normally with a non-zero status
is treated as a fatal configuration/runtime error instead of being retried
forever; EOF or signal termination remains recoverable. Configuration and
listener errors remain fatal. Automatic TCP/UDP mappings are also
process-scoped: they are detached from a failed child and rebound through the
reconnecting session dialer, so a transient relay restart does not leave a
stale Windows listener behind. The
installer copies the built Linux proxy to `~/bin/wsl-proxy-linux`, the
service wrappers to `~/bin/wsl-win-relay-service` and
`~/bin/wsl-win-relay-broker-service`, the strict-listen launcher to
`~/bin/wsl-win-relay-run`, and the kernel supervisor to
`~/bin/wsl-win-relay-strict`; when the native library is present it also
installs it under `~/lib`. It creates a private
`${XDG_CONFIG_HOME:-~/.config}/wsl-win-relay/config.json` from the example only
when one does not already exist. It rejects symlinked/non-regular config paths
and enforces directory mode `0700` and file mode `0600` on every run. Build with `scripts/build-wsl.sh` first
and set the Windows `relay_exe` path in the config.
The installed launcher resolves its sibling supervisor in `~/bin` and the
shared library in `~/lib` automatically; no path overrides are required for
the standard layout.

The startup handshake timeout defaults to five seconds and can be adjusted with
`relay_handshake_timeout` or `-relay-handshake-timeout` when the Windows relay
needs longer to start after a system/network recovery. This timeout only covers
capability negotiation; connection establishment has its separate
`relay_dial_timeout` setting.

Relay-session recovery intentionally starts a fresh child and loses existing
connections; it does not try to reuse protocol state from a broken stdio
transport. See [ADR-0010](docs/adr/0010-relay-failure-supervision.md) for the
failure contract and the requirements for a future in-process hot reconnect.
