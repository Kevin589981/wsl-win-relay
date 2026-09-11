# wsl-win-relay

An emergency WSL-to-Windows network relay for cases where WSL networking is broken but Windows still has connectivity.

The first release exposes a loopback SOCKS5 proxy inside WSL. A Windows helper process performs outbound TCP connections, and the two processes exchange multiplexed frames over stdin/stdout. The design keeps protocol, transport, relay, and user-facing adapters independent so transparent TCP/UDP adapters can be added later.

## Status

The repository is under active implementation. The initial milestone is SOCKS5 CONNECT for IPv4, IPv6, and domain targets.

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

## Build

Build the Linux proxy and Windows relay from the repository root:

```bash
go build -o bin/wsl-proxy ./cmd/wsl-proxy
GOOS=windows GOARCH=amd64 go build -o bin/wsl-win-relay.exe ./cmd/win-relay
```

Place `wsl-win-relay.exe` somewhere visible to WSL interop (or pass its absolute path with `-relay-exe`) and start:

```bash
./bin/wsl-proxy -relay-exe /mnt/c/Users/<user>/bin/wsl-win-relay.exe
curl --proxy socks5h://127.0.0.1:1080 https://example.com
```

The `socks5h` form is intentional: the hostname is sent through the relay and resolved by Windows rather than by WSL.
