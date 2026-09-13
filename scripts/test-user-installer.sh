#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

if [ ! -x "$repo_dir/bin/wsl-proxy-linux" ]; then
    echo "user installer test skipped: build-wsl.sh has not produced wsl-proxy-linux"
    exit 0
fi
if [ ! -x "$repo_dir/bin/wsl-win-relay-strict" ]; then
    echo "user installer test skipped: build-wsl.sh has not produced wsl-win-relay-strict"
    exit 0
fi

mkdir -p "$tmp_dir/home" "$tmp_dir/config" "$tmp_dir/bin"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$tmp_dir/bin/systemctl"
chmod 700 "$tmp_dir/bin/systemctl"

HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
    "$repo_dir/scripts/install-user-service.sh" >"$tmp_dir/install.log"

[ -x "$tmp_dir/home/bin/wsl-proxy-linux" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-run" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-strict" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-service" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-broker-service" ]
[ -f "$tmp_dir/config/wsl-win-relay/config.json" ]
[ "$(stat -c '%a' "$tmp_dir/config/wsl-win-relay/config.json")" = 600 ]

set +e
HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/home/bin:/usr/bin:/bin" \
WSL_WIN_RELAY_CONTROL="$tmp_dir/missing-control.sock" \
    "$tmp_dir/home/bin/wsl-win-relay-run" --kernel /bin/true >"$tmp_dir/launcher.out" 2>&1
launcher_status=$?
set -e
[ "$launcher_status" -eq 0 ]
! grep -q "kernel strict supervisor not found" "$tmp_dir/launcher.out"

set +e
HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/home/bin:/usr/bin:/bin" \
WSL_WIN_RELAY_CONTROL="$tmp_dir/missing-control.sock" \
    "$tmp_dir/home/bin/wsl-win-relay-run" /bin/true >"$tmp_dir/preload.out" 2>&1
preload_status=$?
set -e
[ "$preload_status" -ne 0 ]
! grep -q "listen interposer not found" "$tmp_dir/preload.out"

echo "user installer includes runnable strict supervisor and protected configuration"
