#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

if ! command -v gcc >/dev/null 2>&1 || ! command -v readelf >/dev/null 2>&1; then
    echo "launcher preflight test skipped: gcc/readelf unavailable"
    exit 0
fi

printf '%s\n' \
    '#include <unistd.h>' \
    'int main(void) { return (int)getpid() == 0; }' >"$tmp_dir/target.c"
if ! gcc -static -O2 -o "$tmp_dir/static-target" "$tmp_dir/target.c" 2>"$tmp_dir/build.err"; then
    echo "launcher preflight test skipped: static libc unavailable"
    exit 0
fi

if WSL_WIN_RELAY_CONTROL=/tmp/wwr-no-control "$repo_dir/scripts/wsl-win-relay-run" "$tmp_dir/static-target" >"$tmp_dir/out" 2>"$tmp_dir/err"; then
    echo "static target unexpectedly passed strict launcher preflight" >&2
    exit 1
fi
grep -q 'dynamically linked ELF target' "$tmp_dir/err"

chmod u+s "$tmp_dir/static-target"
if WSL_WIN_RELAY_CONTROL=/tmp/wwr-no-control "$repo_dir/scripts/wsl-win-relay-run" "$tmp_dir/static-target" >"$tmp_dir/out" 2>"$tmp_dir/err"; then
    echo "setuid target unexpectedly passed strict launcher preflight" >&2
    exit 1
fi
grep -q 'setuid/setgid target' "$tmp_dir/err"
echo "strict launcher rejects static and setuid targets before interposition"
