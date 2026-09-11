# wsl-win-relay

An emergency WSL-to-Windows network relay for cases where WSL networking is broken but Windows still has connectivity.

The first release exposes a loopback SOCKS5 proxy inside WSL. A Windows helper process performs outbound TCP connections, and the two processes exchange multiplexed frames over stdin/stdout. The design keeps protocol, transport, relay, and user-facing adapters independent so transparent TCP/UDP adapters can be added later.

## Status

The repository is under active implementation. The current milestone is usable and tested:

- Versioned, bounded multiplexed protocol with explicit stream lifecycle.
- Stdio transport for WSL-to-Windows process interop.
- Windows-side WinSock TCP dialing, including Windows-side DNS for domain targets.
- Loopback SOCKS5 no-auth proxy with IPv4, IPv6, and domain CONNECT.
- TCP half-close propagation so TLS/HTTP clients can finish writes before reading responses.
- Cross-platform builds and WSL interop integration coverage.
- Verified in the target failure mode: WSL could not reach the configured Windows proxy port, while this relay still reached the public Internet and cloned a GitHub repository.

The next milestone is explicit reverse port forwarding: Windows listens on a chosen port and forwards accepted connections to a chosen WSL destination through the same relay. Transparent automatic discovery of arbitrary WSL `listen(2)` calls is intentionally a later layer because a user-space process cannot safely steal an already-bound port without kernel/routing support.

## Security model

- The WSL listener binds to `127.0.0.1` by default.
- The Windows process reads commands only from its parent process pipes.
- There is no proxy authentication in the first milestone; do not bind the listener to a LAN address.
- The relay is intended for the same user's WSL and Windows processes, not as a general network service.

## Planned layers

```text
SOCKS5 / future transparent adapters
              |
       multiplexed relay
              |
   stdio / future transports
              |
      Windows WinSock
```

See [the implementation plan](docs/plans/2026-09-12-wsl-win-relay.md) and [architecture ADR](docs/adr/0001-layered-relay-architecture.md).

## Port direction semantics

Outbound proxy connections do **not** need matching ports. For a request such as `curl -> example.com:443`, WSL only sends the destination; Windows creates an ordinary outbound socket and chooses an ephemeral source port. Source-port correspondence would add no useful information and would create avoidable collisions.

Inbound exposure is different. A Windows port must be bound before Windows clients can connect. The planned reverse-forward command will therefore look like `windows-port:WSL-address`, for example `8000:127.0.0.1:8000`; the WSL application keeps owning its local `8000`, while the relay owns Windows `8000` and connects to the WSL application for each accepted connection.

## Build

Build the Linux proxy and Windows relay from the repository root. From Windows PowerShell:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; go build -o bin/wsl-proxy-linux ./cmd/wsl-proxy
$env:GOOS='windows'; $env:GOARCH='amd64'; go build -o bin/wsl-win-relay.exe ./cmd/win-relay
Remove-Item Env:GOOS,Env:GOARCH
```

`wsl-proxy-linux` is the binary to run inside WSL; `wsl-win-relay.exe` is launched by it through WSL interop. Alternatively, run `go build` for the Linux proxy directly inside WSL.

Place `wsl-win-relay.exe` somewhere visible to WSL interop (or pass its absolute path with `-relay-exe`) and start:

```bash
./bin/wsl-proxy -relay-exe /mnt/c/Users/<user>/bin/wsl-win-relay.exe
curl --proxy socks5h://127.0.0.1:1080 https://example.com
```

The `socks5h` form is intentional: the hostname is sent through the relay and resolved by Windows rather than by WSL.
