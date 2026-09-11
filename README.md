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
- Verified in the target failure mode: WSL could not reach the configured Windows proxy port, while this relay still reached the public Internet and cloned a GitHub repository.

Explicit reverse port forwarding and strict synchronization with dynamically linked application `listen()` calls are implemented. The broader automatic mode remains polling-based so it can support unmodified applications.

Automatic discovery is available as an opt-in polling mode. It mirrors detected TCP listeners after they begin listening. This provides zero-configuration reachability but cannot retroactively make the application's already-successful `listen(2)` fail when Windows rejects the corresponding port; use the strict launcher when rejection propagation is required.

## Security model

- The WSL listener binds to `127.0.0.1` by default.
- The Windows process reads commands only from its parent process pipes.
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

Build the Linux proxy and Windows relay from the repository root. From Windows PowerShell:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; go build -o bin/wsl-proxy-linux ./cmd/wsl-proxy
$env:GOOS='windows'; $env:GOARCH='amd64'; go build -o bin/wsl-win-relay.exe ./cmd/win-relay
Remove-Item Env:GOOS,Env:GOARCH
```

`wsl-proxy-linux` is the binary to run inside WSL; `wsl-win-relay.exe` is launched by it through WSL interop. Alternatively, run `go build` for the Linux proxy directly inside WSL.

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

The `socks5h` form is intentional: TCP hostnames are sent through the relay
and resolved by Windows rather than by WSL. SOCKS5 UDP ASSOCIATE is also
supported; SOCKS5H UDP destinations can remain domain names for upstream
resolution, while native UDP uses Windows resolution. SOCKS fragmentation
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
Windows source. A bind conflict is reported while the mapping starts.
Automatic listener discovery currently remains TCP-only because
`/proc/net/udp` cannot safely distinguish a UDP server socket from an
ephemeral client socket.

To discover WSL listeners dynamically and bind matching Windows loopback ports:

```bash
./bin/wsl-proxy-linux \
  -relay-exe /mnt/d/Code/net/wsl-win-relay/bin/wsl-win-relay.exe \
  -auto-forward
```

Useful controls:

```text
-auto-forward-host 127.0.0.1       Windows bind host; use 0.0.0.0 deliberately for LAN access
-auto-forward-include 8000,9000    Optional allowlist; empty means all discovered ports
-auto-forward-exclude 22,53        Ports that must never be mirrored
-auto-forward-interval 1s          Discovery interval
```

The SOCKS5 listener and explicit reverse-forward destinations are excluded automatically. Automatic mappings are removed when their WSL listener disappears.

## Strict synchronized listen

Build the WSL launcher and interposer with GCC:

```bash
./scripts/build-wsl.sh
```

Keep `wsl-proxy-linux` running, then launch an application through the wrapper:

```bash
./scripts/wsl-win-relay-run python3 -m http.server 8000
```

Before the application's libc `listen()` succeeds, the wrapper reserves
Windows `127.0.0.1:8000`. If Windows reports that the address is already in
use, the application receives Linux `EADDRINUSE` and its `listen()` fails. If
Linux itself rejects the listen, the Windows reservation is aborted. Windows
does not accept clients until both sides have succeeded.

If the control socket is briefly unavailable while the relay is starting or
recovering, the interposer retries the reservation for up to two seconds.
Definitive Windows bind errors are returned immediately.

This propagates bind/listen errors, not later firewall policy. A Windows
firewall rule that drops or rejects clients after the socket is bound does not
make the Windows `bind()` fail, so it cannot be reflected in the original WSL
`listen()` call.

Set `WSL_WIN_RELAY_DEBUG=1` to print control requests and responses from the
interposer. `WSL_WIN_RELAY_CONTROL` and `WSL_WIN_RELAY_PRELOAD` override the
default control socket and shared-library paths.

Strict mode currently covers dynamically linked applications using libc,
including `dup()`, `dup2()`, `dup3()`, `fcntl(F_DUPFD*)`, `close_range()`, and
ordinary `fork()` descriptor inheritance. Static or setuid binaries, programs
making raw syscalls, and `clone()`/`vfork()`-specific ownership patterns should
use automatic polling until a kernel-aware adapter is available. The native
interposer targets the Linux amd64 build produced by the WSL scripts. The
daemon tracks multiple process owners and reaps leases from processes that
exit without closing their descriptors.

The native lifecycle regression test can be run offline with
`./scripts/test-interposer.sh`; it uses a local fake control socket and does
not open a Windows port.

## Transparent mode

For applications without proxy support, install the pinned TUN adapter and keep
the relay running:

```bash
./scripts/install-tun2socks.sh
sudo env WWR_TUN_PROXY=socks5://127.0.0.1:1080 ./scripts/transparent-relay.sh
```

The script creates `tun0`, adds split default routes, starts tun2socks, and
restores routes and the optional DNS file on exit. Set `WWR_DNS=1.1.1.1` when
WSL DNS is unavailable; set `WWR_UPLINK_INTERFACE` if the default interface
cannot be detected. Root, `iproute2`, `/dev/net/tun`, and tun2socks are required.
The script searches `PATH` and the Go `GOPATH/bin` installation location; set
`WWR_TUN2SOCKS_BIN` when running under `sudo` or another environment with a
different tool path.

## Long-running service

With WSL systemd enabled, install the user service:

```bash
./scripts/install-user-service.sh
systemctl --user status wsl-win-relay.service
```

The service restarts the proxy after a Windows relay crash; startup handshake,
reverse registrations, and control sockets are recreated on each restart. The
proxy also retries a relay-only EOF on its own with a two-second backoff when
run directly, while configuration and listener errors remain fatal. The
installer copies the built Linux proxy to `~/bin/wsl-proxy-linux` and the
strict-listen launcher to `~/bin/wsl-win-relay-run`; when the native library is
present it also installs it under `~/lib`. It then creates a private
`~/.config/wsl-win-relay/config.json` from the example only when one does not
already exist. It rejects symlinked/non-regular config paths and enforces mode
`0600` on every run. Build with `scripts/build-wsl.sh` first and set the
Windows `relay_exe` path in the config.

Relay-session recovery intentionally starts a fresh child and loses existing
connections; it does not try to reuse protocol state from a broken stdio
transport. See [ADR-0010](docs/adr/0010-relay-failure-supervision.md) for the
failure contract and the requirements for a future in-process hot reconnect.
