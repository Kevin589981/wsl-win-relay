#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

if WSL_WIN_RELAY_GOARCH=mips64 WSL_WIN_RELAY_OUTPUT_DIR="$tmp_dir/invalid" \
    "$repo_dir/scripts/build-wsl.sh" >"$tmp_dir/invalid.out" 2>&1; then
    echo "unsupported architecture unexpectedly built" >&2
    exit 1
fi
grep -q 'unsupported WSL_WIN_RELAY_GOARCH' "$tmp_dir/invalid.out"

WSL_WIN_RELAY_GOARCH=arm64 WSL_WIN_RELAY_OUTPUT_DIR="$tmp_dir/arm64" \
    "$repo_dir/scripts/build-wsl.sh"

linux_binary=$(file "$tmp_dir/arm64/bin/wsl-proxy-linux")
windows_binary=$(file "$tmp_dir/arm64/bin/wsl-win-relay.exe")
printf '%s\n' "$linux_binary" | grep -Eiq 'ARM aarch64|aarch64'
printf '%s\n' "$windows_binary" | grep -Eiq 'Aarch64|ARM64'

echo "arm64 Linux and Windows Go artifacts cross-build successfully"
