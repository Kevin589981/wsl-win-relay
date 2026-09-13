#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
proxy=$repo_dir/bin/wsl-proxy-linux
strict=$repo_dir/bin/wsl-win-relay-strict
status=$repo_dir/bin/wsl-win-relay-status

for binary in "$proxy" "$strict" "$status"; do
    [ -x "$binary" ] || { echo "missing build output: $binary" >&2; exit 1; }
done

proxy_version=$($proxy -version)
strict_version=$($strict -version)
status_version=$($status -version)
version=$(printf '%s\n' "$proxy_version" | awk '{print $2}')
commit=$(printf '%s\n' "$proxy_version" | sed -n 's/.*(commit \([^,]*\), built .*/\1/p')
built_at=$(printf '%s\n' "$proxy_version" | sed -n 's/.*built \([^)]*\)).*/\1/p')

[ -n "$version" ] && [ -n "$commit" ] && [ -n "$built_at" ]
if command -v git >/dev/null 2>&1 && git -C "$repo_dir" rev-parse --git-dir >/dev/null 2>&1; then
    expected_version=$(git -c core.autocrlf=true -C "$repo_dir" describe --tags --always --dirty)
    [ "$version" = "$expected_version" ] || {
        echo "embedded version $version does not match normalized worktree version $expected_version" >&2
        exit 1
    }
fi
printf '%s\n' "$strict_version" | grep -F "wsl-win-relay-strict $version (commit $commit, built $built_at)" >/dev/null
printf '%s\n' "$status_version" | grep -F "wsl-win-relay-status $version (commit $commit, built $built_at)" >/dev/null
for executable in wsl-win-relay.exe wsl-win-broker.exe wsl-win-connector.exe; do
    metadata=$(go version -m "$repo_dir/bin/$executable")
    printf '%s\n' "$metadata" | grep -F "buildinfo.Version=$version" >/dev/null
    printf '%s\n' "$metadata" | grep -F "buildinfo.Commit=$commit" >/dev/null
    printf '%s\n' "$metadata" | grep -F "buildinfo.BuiltAt=$built_at" >/dev/null
done

invalid_output=$(mktemp -d)
trap 'rm -rf "$invalid_output"' EXIT INT TERM
if WSL_WIN_RELAY_OUTPUT_DIR="$invalid_output" SOURCE_DATE_EPOCH=invalid "$repo_dir/scripts/build-wsl.sh" >/dev/null 2>&1; then
    echo "build accepted an invalid SOURCE_DATE_EPOCH" >&2
    exit 1
fi
rm -rf "$invalid_output"
trap - EXIT INT TERM

echo "all relay executables carry matching build metadata: $version $commit $built_at"
