#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output_dir=${WSL_WIN_RELAY_OUTPUT_DIR:-$repo_dir}
mkdir -p "$output_dir/bin" "$output_dir/lib"

version=${WSL_WIN_RELAY_BUILD_VERSION:-$(git -C "$repo_dir" describe --tags --always --dirty 2>/dev/null || printf dev)}
commit=${WSL_WIN_RELAY_BUILD_COMMIT:-$(git -C "$repo_dir" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)}
if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
    built_at=$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null) || {
        echo "invalid SOURCE_DATE_EPOCH: $SOURCE_DATE_EPOCH" >&2
        exit 1
    }
else
    built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fi
for value in "$version" "$commit" "$built_at"; do
    case "$value" in
        ''|*[!A-Za-z0-9._:+TZ-]*) echo "invalid build metadata value: $value" >&2; exit 1 ;;
    esac
done
go_ldflags="-X github.com/Kevin589981/wsl-win-relay/internal/buildinfo.Version=$version -X github.com/Kevin589981/wsl-win-relay/internal/buildinfo.Commit=$commit -X github.com/Kevin589981/wsl-win-relay/internal/buildinfo.BuiltAt=$built_at"

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

GOTOOLCHAIN=local GOOS=linux GOARCH="$go_arch" go build -ldflags "$go_ldflags" -o "$output_dir/bin/wsl-proxy-linux" "$repo_dir/cmd/wsl-proxy"
GOTOOLCHAIN=local GOOS=linux GOARCH="$go_arch" go build -ldflags "$go_ldflags" -o "$output_dir/bin/wsl-win-relay-status" "$repo_dir/cmd/wsl-status"
GOTOOLCHAIN=local GOOS=windows GOARCH="$go_arch" go build -ldflags "$go_ldflags" -o "$output_dir/bin/wsl-win-relay.exe" "$repo_dir/cmd/win-relay"
GOTOOLCHAIN=local GOOS=windows GOARCH="$go_arch" go build -ldflags "$go_ldflags" -o "$output_dir/bin/wsl-win-broker.exe" "$repo_dir/cmd/win-broker"
GOTOOLCHAIN=local GOOS=windows GOARCH="$go_arch" go build -ldflags "$go_ldflags" -o "$output_dir/bin/wsl-win-connector.exe" "$repo_dir/cmd/win-connector"
gcc -O2 -Wall -Wextra -Werror -fPIC -shared \
    -o "$output_dir/lib/libwsl_win_relay_listen.so" \
    "$repo_dir/native/listen_interposer.c" -ldl -pthread
gcc -O2 -Wall -Wextra -Werror -std=c11 \
    -DWWR_VERSION="\"$version\"" -DWWR_COMMIT="\"$commit\"" -DWWR_BUILT_AT="\"$built_at\"" \
    -o "$output_dir/bin/wsl-win-relay-strict" \
    "$repo_dir/native/strict_supervisor.c"
