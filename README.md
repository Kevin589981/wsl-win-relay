# wsl-win-relay

An emergency network relay for WSL when WSL networking is broken but Windows
still has network access.

WSL applications connect to a local SOCKS5 or HTTP proxy. A Windows process
performs the real outbound TCP/UDP connection, optionally through a Windows-side
upstream proxy. WSL therefore does not need to reach the Windows proxy directly.
This is useful when HNS, mirrored networking, a Windows hotspot, or the Windows
loopback path is malfunctioning.

The relay can also expose WSL services on Windows ports, coordinate strict
`listen()`/`bind()` calls, and route proxy-unaware applications through an
optional TUN adapter.

## Features

- Loopback SOCKS5 proxy for proxy-aware command-line tools.
- Optional HTTP proxy for tools that do not support SOCKS5.
- Windows WinSock egress, or a Windows HTTP/SOCKS5 upstream proxy.
- SOCKS5 UDP ASSOCIATE and reverse UDP forwarding.
- Explicit reverse TCP/UDP mappings from Windows to WSL.
- Polling-based automatic TCP listener discovery.
- Conservative allowlisted automatic UDP discovery.
- Strict Windows-before-WSL TCP `listen()` and UDP `bind()` coordination.
- Optional TUN/tun2socks mode for applications without proxy support.
- Persistent broker mode with connector reconnect and socket-owner recovery.
- Idempotent systemd user-service installation and protected credentials.

## How It Works

```text
WSL application
      |
127.0.0.1:1080 or 127.0.0.1:8080
      |
WSL relay client
      |
stdio child or broker connector
      |
Windows relay / socket owner
      |
WinSock direct or Windows upstream proxy
```

For an outbound request, ports do not need to correspond. WSL sends the target
address; Windows creates a normal outbound socket and chooses an ephemeral
source port. Inbound exposure is different: Windows must bind a listening port
and then open a connection to the WSL service.

## Limitations

This project does not repair HNS. WSL must still be able to launch a Windows
executable through WSL interop, and Windows must be able to reach the internet
or the configured upstream proxy.

Automatic discovery is polling-based. It creates a Windows mapping after a WSL
process has successfully called `listen()`. It cannot make that earlier call
fail if Windows later refuses the port. Use strict mode when the refusal must
be returned synchronously to the WSL process.

The default SOCKS5 and HTTP listeners bind WSL loopback and have no client
authentication. Do not expose them to a LAN without adding a separate firewall
and authentication boundary.

## Requirements

- Windows with WSL and WSL interop enabled.
- An amd64 WSL distribution for the documented runtime verification.
- Go 1.22 or newer to build from source.
- GCC for the native strict supervisor and optional LD_PRELOAD interposer.
- WSL systemd only when using the service installers.
- Root, `iproute2`, `/dev/net/tun`, and tun2socks only for transparent mode.

The repository may live on a Windows-mounted drive. The examples use:

```text
D:\Code\net\wsl-win-relay
/mnt/d/Code/net/wsl-win-relay
```

For a new checkout, use:

```bash
git clone https://github.com/Kevin589981/wsl-win-relay.git
cd wsl-win-relay
```

The repository is private, so the GitHub account running `git clone` must have
access to it.

## Quick Start: SOCKS5

This mode needs no systemd. It starts a Windows relay child from WSL.

### 1. Build

```bash
cd /mnt/d/Code/net/wsl-win-relay
./scripts/build-wsl.sh
```

The relevant files are created in `bin/`:

```text
wsl-proxy-linux       WSL proxy
wsl-win-relay.exe     Windows stdio relay
wsl-win-broker.exe    Windows persistent broker
wsl-win-connector.exe WSL-to-broker connector
wsl-win-relay-status  mapping status reader
```

### 2. Configure

```bash
cp wsl-win-relay.example.json "$HOME/wsl-win-relay.json"
${EDITOR:-nano} "$HOME/wsl-win-relay.json"
```

For your setup, the important values are:

```json
{
  "relay_exe": "/mnt/d/Code/net/wsl-win-relay/bin/wsl-win-relay.exe",
  "broker_mode": false,
  "upstream_proxy": "socks5h://matebookxpro.local:7890",
  "socks5_listen": "127.0.0.1:1080",
  "http_proxy_listen": "127.0.0.1:8080"
}
```

`upstream_proxy` is used by the Windows process. WSL does not connect to
`matebookxpro.local:7890` itself. Leave it empty when Windows should connect
directly.

### 3. Start and use

```bash
./bin/wsl-proxy-linux -config "$HOME/wsl-win-relay.json"
```

Keep that terminal running. In another WSL terminal:

```bash
curl --proxy socks5h://127.0.0.1:1080 https://example.com
```

For tools that only support HTTP proxies:

```bash
HTTPS_PROXY=http://127.0.0.1:8080 curl https://example.com
HTTP_PROXY=http://127.0.0.1:8080 curl http://example.com
```

Use `socks5h` when the target hostname should be resolved through the relay.
Use `socks5` when Windows should resolve it before sending an IP address to the
upstream SOCKS5 proxy.

## Recommended Setup: Persistent Broker

Broker mode keeps Windows-side socket ownership alive while the WSL connector,
broker frontend, or bridge processes restart. WSL systemd must be enabled.

```bash
cd /mnt/d/Code/net/wsl-win-relay
./scripts/build-wsl.sh

export WSL_WIN_RELAY_BROKER_EXE=/mnt/d/Code/net/wsl-win-relay/bin/wsl-win-broker.exe
export WSL_WIN_RELAY_CONNECTOR_EXE=/mnt/d/Code/net/wsl-win-relay/bin/wsl-win-connector.exe
export WSL_WIN_RELAY_UPSTREAM_PROXY=socks5h://matebookxpro.local:7890

./scripts/install-broker-user-service.sh
./scripts/install-user-service.sh
```

The installers create protected files under `~/.config/wsl-win-relay/`:

```text
config.json   WSL proxy configuration
broker.env    Windows broker path, endpoint, upstream, and mode
attach.token  broker authentication token
```

Check the services and test the local proxy:

```bash
systemctl --user status wsl-win-relay-broker.service
systemctl --user status wsl-win-relay.service
curl --proxy socks5h://127.0.0.1:1080 https://example.com
```

In broker mode, put the upstream proxy in `broker.env`, not in the WSL proxy's
`upstream_proxy` setting. The connector carries the authenticated relay stream;
the Windows broker performs outbound dialing.

## Configuration Reference

The full sample is [`wsl-win-relay.example.json`](wsl-win-relay.example.json).
Validate it without starting a relay or binding a port:

```bash
./bin/wsl-proxy-linux \
  -config "$HOME/.config/wsl-win-relay/config.json" \
  -check-config
```

Common fields:

| Field | Default | Purpose |
| --- | --- | --- |
| `socks5_listen` | `127.0.0.1:1080` | WSL SOCKS5 listener |
| `http_proxy_listen` | `127.0.0.1:8080` | WSL HTTP listener |
| `relay_handshake_timeout` | `5s` | startup capability handshake |
| `relay_dial_timeout` | `30s` | wait for a usable relay session |
| `proxy_handshake_timeout` | `15s` | local SOCKS/HTTP handshake limit |
| `max_proxy_connections` | `256` | clients per local proxy listener |
| `udp_associate_idle_timeout` | `5m` | idle SOCKS5 UDP association limit |
| `control_socket` | `/tmp/wsl-win-relay-control.sock` | strict control socket |

Unknown JSON fields are rejected. Command-line scalar values override JSON;
repeated `-reverse` and `-reverse-udp` flags add mappings.

## Reverse Port Forwarding

### Explicit TCP mapping

If a WSL service listens on `127.0.0.1:8000`, add this to the configuration:

```json
{
  "reverse": [
    "127.0.0.1:8000=127.0.0.1:8000"
  ]
}
```

The left side is the Windows bind address. The right side is the WSL target.
Windows owns its listening port and opens a fresh WSL connection for each
Windows client:

```powershell
Invoke-WebRequest http://127.0.0.1:8000/
```

A Windows bind failure is reported during mapping setup and does not leave a
half-installed mapping. A firewall may still reject clients after a successful
bind; that later policy is not observable as a WSL `listen()` error.

### Explicit UDP mapping

```json
{
  "reverse_udp": [
    "127.0.0.1:5353=127.0.0.1:5353"
  ]
}
```

Windows source endpoints are isolated into separate flows and responses return
to the source that sent each datagram.

## Automatic Mapping

Enable polling-based TCP discovery:

```json
{
  "auto_forward": {
    "enabled": true,
    "windows_host": "127.0.0.1",
    "windows_host6": "::1",
    "interval": "1s",
    "exclude": [22, 53]
  }
}
```

Or use flags for a one-off process:

```bash
./bin/wsl-proxy-linux \
  -config "$HOME/wsl-win-relay.json" \
  -auto-forward \
  -auto-forward-include 8000,9000 \
  -auto-forward-exclude 22,53
```

When mirrored networking shares the WSL and Windows port namespace, choose a
deterministic offset:

```text
-auto-forward-port-offset 10000
```

A WSL listener on `8000` is then exposed on Windows `10800`.

To let Windows choose a free port and publish the result:

```json
{
  "auto_forward": {
    "enabled": true,
    "windows_port_auto": true,
    "status_file": "/run/user/1000/wsl-win-relay-mappings.json"
  }
}
```

```bash
./bin/wsl-win-relay-status \
  -file /run/user/1000/wsl-win-relay-mappings.json
```

Mappings are removed when their WSL listener disappears. Windows bind refusals
use bounded retry backoff.

### Allowlisted UDP discovery

General UDP discovery is unsafe because `/proc/net/udp` cannot distinguish a
server socket from an ephemeral client socket. Opt in only for known ports:

```json
{
  "auto_forward": {
    "enabled": true,
    "udp_enabled": true,
    "udp_include": [5353, 8125]
  }
}
```

## Strict Synchronized Listen/Bind

Use strict mode when Windows must reserve the port before WSL observes
`listen()` or non-zero UDP `bind()` success:

```bash
~/bin/wsl-win-relay-run python3 -m http.server 8000
```

For a complete shell process tree:

```bash
~/bin/wsl-win-relay-shell
python3 -m http.server 8000
```

If Windows reports `EADDRINUSE`, the WSL call fails with `EADDRINUSE`. If WSL
rejects the call, the Windows reservation is aborted. This is the mode that
implements synchronous refusal propagation.

Static applications use the kernel adapter:

```bash
~/bin/wsl-win-relay-run --kernel ./static-service 8000
```

The strict limits can be adjusted in JSON:

```json
{
  "strict_max_connections": 64,
  "strict_max_leases": 512,
  "strict_max_owners_per_lease": 256
}
```

The kernel adapter is runtime-verified on Linux amd64. aarch64 is build-only.
Setuid/setgid binaries are rejected because their privilege semantics cannot be
preserved safely by these adapters.

## Transparent TUN Mode

Use this for applications with no proxy support. It requires root, `iproute2`,
`/dev/net/tun`, and tun2socks.

```bash
./scripts/install-tun2socks.sh
```

With the local SOCKS5 proxy already running:

```bash
sudo env \
  WWR_TUN_PROXY=socks5://127.0.0.1:1080 \
  ./scripts/transparent-relay.sh
```

When HNS has removed the normal WSL interface and the proxy is local:

```bash
sudo env \
  WWR_UPLINK_INTERFACE=lo \
  WWR_TUN_PROXY=socks5://127.0.0.1:1080 \
  ./scripts/transparent-relay.sh
```

The script waits for the local proxy before changing routes and restores routes,
DNS, and the TUN device on exit. Set `WWR_DNS=1.1.1.1` only when replacement
DNS is required.

For a boot-persistent transparent service:

```bash
sudo env WWR_TUN2SOCKS_BIN="$(go env GOPATH)/bin/tun2socks" \
  ./scripts/install-transparent-service.sh
sudo systemctl status wsl-win-relay-transparent.service
```

## Operations

Follow logs:

```bash
journalctl --user -u wsl-win-relay.service -f
journalctl --user -u wsl-win-relay-broker.service -f
```

Run diagnostics:

```bash
~/bin/wsl-win-relay-doctor
~/bin/wsl-win-relay-doctor --probe-url https://example.com
```

Stop services:

```bash
systemctl --user stop wsl-win-relay.service
systemctl --user stop wsl-win-relay-broker.service
```

Uninstall the unprivileged deployment while preserving configuration and
credentials:

```bash
./scripts/install-user-service.sh --uninstall
```

Uninstall the privileged transparent service:

```bash
sudo ./scripts/install-transparent-service.sh --uninstall
```

## Optional Windows Task Scheduler

If the broker should remain available across WSL user-service or VM shutdown,
install the host-level supervisor from PowerShell 7. Use the Windows paths to
the broker executable and the token file:

```powershell
.\scripts\install-broker-windows-task.ps1 `
  -BrokerExe 'C:\Tools\wsl-win-broker.exe' `
  -TokenFile 'C:\Users\you\.config\wsl-win-relay\attach.token' `
  -StartNow
```

Remove the task with the same task name and paths:

```powershell
.\scripts\install-broker-windows-task.ps1 `
  -BrokerExe 'C:\Tools\wsl-win-broker.exe' `
  -TokenFile 'C:\Users\you\.config\wsl-win-relay\attach.token' `
  -Uninstall
```

This keeps future broker attachments available. It cannot preserve established
streams after the socket-owner process itself crashes.

## Troubleshooting

### WSL cannot reach `matebookxpro.local:7890`

That direct connection is not required. Check:

1. WSL can launch a Windows executable through interop.
2. The Windows broker is running.
3. Broker mode has `WSL_WIN_RELAY_UPSTREAM_PROXY` in `broker.env`.
4. Windows itself can resolve and reach `matebookxpro.local:7890`.
5. WSL can reach the local listener `127.0.0.1:1080`.

The intended path is:

```text
WSL application -> WSL 127.0.0.1:1080 -> Windows broker -> matebookxpro.local:7890
```

### PowerShell is not found from WSL

This only affects some interop smoke tests and cleanup helpers. Set its mounted
absolute path:

```bash
export WWR_WINDOWS_SHELL=/mnt/d/AppGallery/Downloads/PowerShell/7/pwsh.exe
```

### A Windows port is already in use

Mirrored networking can share the port namespace. For automatic mappings use a
port offset or Windows automatic allocation. For synchronous rejection use
strict mode.

### The service does not start

```bash
~/bin/wsl-win-relay-doctor
./bin/wsl-proxy-linux \
  -config "$HOME/.config/wsl-win-relay/config.json" \
  -check-config
```

Check the Windows broker and connector paths, and ensure their build metadata
matches. Do not set `WSL_WIN_RELAY_ALLOW_UNVERIFIED_BINARIES=1` unless using
legacy or deliberately custom binaries.

## Verification

Run the repeatable amd64 WSL release gate:

```bash
./scripts/test-release.sh
```

It runs Go tests, vet, race detection, amd64 builds, native strict tests,
transparent rollback, installer tests, and the broker recovery matrix.

Run the full Windows interop gate with the current environment:

```bash
WWR_WINDOWS_SHELL=/mnt/d/AppGallery/Downloads/PowerShell/7/pwsh.exe \
WWR_WINDOWS_UPSTREAM_PROXY=socks5h://matebookxpro.local:7890 \
WWR_BROKER_INTEROP_SERVICE_WRAPPER=1 \
WWR_BROKER_INTEROP_WINDOWS_PORT_AUTO=1 \
WWR_BROKER_INTEROP_AUTO_UDP=1 \
  ./scripts/test-release.sh --windows-interop
```

This verifies SOCKS5/HTTP egress, Windows-side upstream proxy use, explicit
and automatic TCP/UDP mappings, connector reconnect, broker recovery, and
mapping cleanup. aarch64 remains build-only by design.

## Development

```bash
gofmt -w $(find cmd internal -type f -name '*.go')
go test ./...
go vet ./...
go test -race ./...
```

The repository is vendored. The release gate uses `GOPROXY=off` for isolated
checks. See the [implementation plan](docs/plans/2026-09-12-wsl-win-relay.md)
and [architecture decisions](docs/adr/) for design details.

## Security

- WSL SOCKS5/HTTP listeners bind loopback by default and have no authentication.
- Broker and role endpoints use same-user local IPC, not LAN listeners.
- Attach tokens and service environment files are protected with mode `0600`.
- Do not put secrets in public command lines or expose the local proxy to a LAN.
- Treat the relay as a same-user WSL/Windows boundary, not a general network
  service.

## License

Copyright 2026 Kevin589981.

This project is licensed under the Apache License, Version 2.0. See
[`LICENSE`](LICENSE). Vendored dependencies retain their own license notices.
