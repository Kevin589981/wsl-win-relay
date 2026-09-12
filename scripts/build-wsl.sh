#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$repo_dir/bin" "$repo_dir/lib"

go_arch=${WSL_WIN_RELAY_GOARCH:-}
if [ -z "$go_arch" ]; then
    case "$(uname -m)" in
        x86_64|amd64) go_arch=amd64 ;;
        aarch64|arm64) go_arch=arm64 ;;
        *)
            echo "unsupported WSL host architecture: $(uname -m) (set WSL_WIN_RELAY_GOARCH explicitly)" >&2
            exit 1
            ;;
    esac
fi
case "$go_arch" in
    amd64|arm64) ;;
    *) echo "unsupported WSL_WIN_RELAY_GOARCH: $go_arch (expected amd64 or arm64)" >&2; exit 1 ;;
esac

GOTOOLCHAIN=local GOOS=linux GOARCH="$go_arch" go build -o "$repo_dir/bin/wsl-proxy-linux" "$repo_dir/cmd/wsl-proxy"
GOTOOLCHAIN=local GOOS=windows GOARCH="$go_arch" go build -o "$repo_dir/bin/wsl-win-relay.exe" "$repo_dir/cmd/win-relay"
GOTOOLCHAIN=local GOOS=windows GOARCH="$go_arch" go build -o "$repo_dir/bin/wsl-win-broker.exe" "$repo_dir/cmd/win-broker"
GOTOOLCHAIN=local GOOS=windows GOARCH="$go_arch" go build -o "$repo_dir/bin/wsl-win-connector.exe" "$repo_dir/cmd/win-connector"
gcc -O2 -Wall -Wextra -Werror -fPIC -shared \
    -o "$repo_dir/lib/libwsl_win_relay_listen.so" \
    "$repo_dir/native/listen_interposer.c" -ldl -pthread
gcc -O2 -Wall -Wextra -Werror -std=c11 \
    -o "$repo_dir/bin/wsl-win-relay-strict" \
    "$repo_dir/native/strict_supervisor.c"
