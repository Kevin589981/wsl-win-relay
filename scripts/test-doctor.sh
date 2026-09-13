#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp_dir/home/bin" "$tmp_dir/config/wsl-win-relay" "$tmp_dir/windows" "$tmp_dir/fake-bin"
config="$tmp_dir/config/wsl-win-relay/config.json"
broker_env="$tmp_dir/config/wsl-win-relay/broker.env"
token_file="$tmp_dir/config/wsl-win-relay/attach.token"

printf '%s\n' '{}' >"$config"
printf '%s\n' '00112233445566778899aabbccddeeff' >"$token_file"
chmod 600 "$config" "$token_file"

printf '%s\n' '#!/bin/sh' '[ "${FAKE_PROXY_FAIL:-0}" = 0 ]' >"$tmp_dir/home/bin/wsl-proxy-linux"
printf '%s\n' '#!/bin/sh' 'printf "%s\n" "wsl-win-broker test-build"' >"$tmp_dir/windows/wsl-win-broker.exe"
printf '%s\n' '#!/bin/sh' 'printf "%s\n" "wsl-win-connector test-build"' >"$tmp_dir/windows/wsl-win-connector.exe"
chmod 700 "$tmp_dir/home/bin/wsl-proxy-linux" "$tmp_dir/windows/wsl-win-broker.exe" "$tmp_dir/windows/wsl-win-connector.exe"
printf '%s\n' '#!/bin/sh' \
    'case "$*" in' \
    '  "--user show-environment"|"--user cat "*) exit 0 ;;' \
    '  "--user is-active --quiet "*) [ "${FAKE_SYSTEMD_INACTIVE:-0}" = 0 ] ;;' \
    '  *) exit 1 ;;' \
    'esac' >"$tmp_dir/fake-bin/systemctl"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$tmp_dir/fake-bin/curl"
chmod 700 "$tmp_dir/fake-bin/systemctl" "$tmp_dir/fake-bin/curl"

{
    printf "WSL_WIN_RELAY_BROKER_EXE='%s'\n" "$tmp_dir/windows/wsl-win-broker.exe"
    printf "WSL_WIN_RELAY_CONNECTOR_EXE='%s'\n" "$tmp_dir/windows/wsl-win-connector.exe"
    printf "WSL_WIN_RELAY_ATTACH_TOKEN='%s'\n" '00112233445566778899aabbccddeeff'
    printf "WSL_WIN_RELAY_ATTACH_TOKEN_FILE='%s'\n" "$token_file"
    printf "WSL_WIN_RELAY_BROKER_MODE='1'\n"
} >"$broker_env"
chmod 600 "$broker_env"

HOME="$tmp_dir/home" XDG_CONFIG_HOME="$tmp_dir/config" \
    "$repo_dir/scripts/wsl-win-relay-doctor" --skip-services >"$tmp_dir/pass.log"
grep -q 'Windows broker executable ran through interop' "$tmp_dir/pass.log"
grep -q 'Windows broker and connector build metadata match' "$tmp_dir/pass.log"
grep -q 'Summary: 0 failure(s)' "$tmp_dir/pass.log"

HOME="$tmp_dir/home" XDG_CONFIG_HOME="$tmp_dir/config" PATH="$tmp_dir/fake-bin:/usr/bin:/bin" \
    "$repo_dir/scripts/wsl-win-relay-doctor" --probe-url https://example.test >"$tmp_dir/services.log"
grep -q 'wsl-win-relay.service is active' "$tmp_dir/services.log"
grep -q 'wsl-win-relay-broker.service is active' "$tmp_dir/services.log"
grep -q 'end-to-end SOCKS probe reached https://example.test' "$tmp_dir/services.log"

set +e
HOME="$tmp_dir/home" XDG_CONFIG_HOME="$tmp_dir/config" PATH="$tmp_dir/fake-bin:/usr/bin:/bin" FAKE_SYSTEMD_INACTIVE=1 \
    "$repo_dir/scripts/wsl-win-relay-doctor" >"$tmp_dir/inactive.log"
inactive_status=$?
set -e
[ "$inactive_status" -ne 0 ]
grep -q 'wsl-win-relay.service is installed but inactive' "$tmp_dir/inactive.log"

printf '%s\n' '#!/bin/sh' 'printf "%s\n" "wsl-win-connector other-build"' >"$tmp_dir/windows/wsl-win-connector.exe"
chmod 700 "$tmp_dir/windows/wsl-win-connector.exe"
set +e
HOME="$tmp_dir/home" XDG_CONFIG_HOME="$tmp_dir/config" \
    "$repo_dir/scripts/wsl-win-relay-doctor" --skip-services >"$tmp_dir/mismatch.log"
mismatch_status=$?
set -e
[ "$mismatch_status" -ne 0 ]
grep -q 'build metadata do not match' "$tmp_dir/mismatch.log"

set +e
HOME="$tmp_dir/home" XDG_CONFIG_HOME="$tmp_dir/config" FAKE_PROXY_FAIL=1 \
    "$repo_dir/scripts/wsl-win-relay-doctor" --skip-services >"$tmp_dir/config-fail.log"
config_status=$?
set -e
[ "$config_status" -ne 0 ]
grep -q 'production configuration parser rejected' "$tmp_dir/config-fail.log"

echo 'doctor validates configuration, credentials, and Windows executable compatibility'
