#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$repo_dir/bin" "$repo_dir/lib"

GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 go build -o "$repo_dir/bin/wsl-proxy-linux" "$repo_dir/cmd/wsl-proxy"
GOTOOLCHAIN=local GOOS=windows GOARCH=amd64 go build -o "$repo_dir/bin/wsl-win-relay.exe" "$repo_dir/cmd/win-relay"
gcc -O2 -Wall -Wextra -Werror -fPIC -shared \
    -o "$repo_dir/lib/libwsl_win_relay_listen.so" \
    "$repo_dir/native/listen_interposer.c" -ldl -pthread
