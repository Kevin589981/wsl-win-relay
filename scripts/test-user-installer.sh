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
if [ ! -x "$repo_dir/bin/wsl-win-relay-status" ]; then
    echo "user installer test skipped: build-wsl.sh has not produced wsl-win-relay-status"
    exit 0
fi
if [ ! -x "$repo_dir/bin/wsl-win-relay-strict" ]; then
    echo "user installer test skipped: build-wsl.sh has not produced wsl-win-relay-strict"
    exit 0
fi

mkdir -p "$tmp_dir/home" "$tmp_dir/config" "$tmp_dir/bin"
printf '%s\n' '#!/bin/sh' 'printf "%s\n" "$*" >>"$SYSTEMCTL_LOG"' >"$tmp_dir/bin/systemctl"
chmod 700 "$tmp_dir/bin/systemctl"

HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
SYSTEMCTL_LOG="$tmp_dir/systemctl.log" \
    "$repo_dir/scripts/install-user-service.sh" >"$tmp_dir/install.log"

[ -x "$tmp_dir/home/bin/wsl-proxy-linux" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-status" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-run" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-shell" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-strict" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-service" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-broker-service" ]
[ -x "$tmp_dir/home/bin/wsl-win-relay-doctor" ]
[ -f "$tmp_dir/config/wsl-win-relay/config.json" ]
[ "$(stat -c '%a' "$tmp_dir/config/wsl-win-relay/config.json")" = 600 ]
"$tmp_dir/home/bin/wsl-proxy-linux" \
    -config "$tmp_dir/config/wsl-win-relay/config.json" \
    -check-config >"$tmp_dir/check.log" 2>"$tmp_dir/check.err"
grep -q '^configuration valid$' "$tmp_dir/check.log"
[ ! -s "$tmp_dir/check.err" ]
grep -q 'daemon-reload' "$tmp_dir/systemctl.log"
! grep -q 'enable wsl-win-relay.service' "$tmp_dir/systemctl.log"
! grep -q 'restart wsl-win-relay.service' "$tmp_dir/systemctl.log"
grep -q 'without starting it' "$tmp_dir/install.log"

printf '%s\n' '{"relay_exe":"/mnt/c/Users/you/bin/wsl-win-relay.exe"}' >"$tmp_dir/config/wsl-win-relay/config.json"
: >"$tmp_dir/systemctl.log"
HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
SYSTEMCTL_LOG="$tmp_dir/systemctl.log" \
    "$repo_dir/scripts/install-user-service.sh" >"$tmp_dir/minified-placeholder.log"
! grep -q 'enable wsl-win-relay.service' "$tmp_dir/systemctl.log"
! grep -q 'restart wsl-win-relay.service' "$tmp_dir/systemctl.log"

printf '%s\n' '{"relay_exe":"/mnt/c/relay.exe"}' >"$tmp_dir/config/wsl-win-relay/config.json"
: >"$tmp_dir/systemctl.log"
HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
SYSTEMCTL_LOG="$tmp_dir/systemctl.log" \
    "$repo_dir/scripts/install-user-service.sh" >"$tmp_dir/reinstall.log"
grep -q 'enable wsl-win-relay.service' "$tmp_dir/systemctl.log"
grep -q 'restart wsl-win-relay.service' "$tmp_dir/systemctl.log"

: >"$tmp_dir/systemctl.log"
printf '%s\n' '{"unknown_setting":true}' >"$tmp_dir/config/wsl-win-relay/config.json"
set +e
HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
SYSTEMCTL_LOG="$tmp_dir/systemctl.log" \
    "$repo_dir/scripts/install-user-service.sh" >"$tmp_dir/invalid-install.log" 2>&1
invalid_status=$?
set -e
[ "$invalid_status" -ne 0 ]
grep -q 'configuration validation failed' "$tmp_dir/invalid-install.log"
[ ! -s "$tmp_dir/systemctl.log" ]
install -m 0600 "$repo_dir/wsl-win-relay.example.json" "$tmp_dir/config/wsl-win-relay/config.json"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$tmp_dir/fake-connector.exe"
chmod 700 "$tmp_dir/fake-connector.exe"
printf '%s\n' \
    'WSL_WIN_RELAY_BROKER_MODE=1' \
    "WSL_WIN_RELAY_CONNECTOR_EXE='$tmp_dir/fake-connector.exe'" \
    >"$tmp_dir/config/wsl-win-relay/broker.env"
chmod 600 "$tmp_dir/config/wsl-win-relay/broker.env"
: >"$tmp_dir/systemctl.log"
HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
SYSTEMCTL_LOG="$tmp_dir/systemctl.log" \
    "$repo_dir/scripts/install-user-service.sh" >"$tmp_dir/broker-first.log"
grep -q 'enable wsl-win-relay.service' "$tmp_dir/systemctl.log"
grep -q 'restart wsl-win-relay.service' "$tmp_dir/systemctl.log"

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

HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/home/bin:/usr/bin:/bin" \
SHELL=/bin/sh \
WSL_WIN_RELAY_SHELL=/bin/sh \
WSL_WIN_RELAY_CONTROL="$tmp_dir/missing-control.sock" \
    "$tmp_dir/home/bin/wsl-win-relay-shell" -c 'exit 0'

echo "user installer includes runnable strict supervisor and protected configuration"
