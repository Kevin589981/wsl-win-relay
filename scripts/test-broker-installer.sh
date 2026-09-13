#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp_dir/home" "$tmp_dir/config" "$tmp_dir/bin"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$tmp_dir/bin/systemctl"
chmod 700 "$tmp_dir/bin/systemctl"
export WSL_WIN_RELAY_CONNECTOR_EXE=/bin/true

HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
WSL_WIN_RELAY_BROKER_EXE=/bin/echo \
WSL_WIN_RELAY_UPSTREAM_PROXY=socks5h://matebookxpro.local:7890 \
    "$repo_dir/scripts/install-broker-user-service.sh" >/dev/null

config_dir=$tmp_dir/config/wsl-win-relay
token_file=$config_dir/attach.token
env_file=$config_dir/broker.env
[ -f "$token_file" ] && [ -f "$env_file" ]
[ "$(stat -c '%a' "$token_file")" = 600 ]
[ "$(stat -c '%a' "$env_file")" = 600 ]
token=$(sed -n "s/^WSL_WIN_RELAY_ATTACH_TOKEN='\([^']*\)'$/\1/p" "$env_file")
[ "${#token}" -eq 64 ]
grep -Fqx "WSL_WIN_RELAY_ATTACH_TOKEN_FILE='$token_file'" "$env_file"
grep -Fqx 'WSL_WIN_RELAY_BROKER_MODE=1' "$env_file"
grep -Fqx "WSL_WIN_RELAY_CONNECTOR_EXE='/bin/true'" "$env_file"
grep -Fqx "WSL_WIN_RELAY_UPSTREAM_PROXY='socks5h://matebookxpro.local:7890'" "$env_file"

if HOME="$tmp_dir/home" \
    XDG_CONFIG_HOME="$tmp_dir/config" \
    PATH="$tmp_dir/bin:/usr/bin:/bin" \
    WSL_WIN_RELAY_BROKER_EXE=/bin/echo \
    WSL_WIN_RELAY_CONNECTOR_EXE=/bin/false \
        "$repo_dir/scripts/install-broker-user-service.sh" >"$tmp_dir/connector-mismatch.out" 2>&1; then
    echo "broker installer unexpectedly accepted a conflicting connector executable" >&2
    exit 1
fi
grep -q 'connector executable does not match' "$tmp_dir/connector-mismatch.out"

HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
WSL_WIN_RELAY_BROKER_EXE=/bin/echo \
WSL_WIN_RELAY_UPSTREAM_PROXY=socks5h://matebookxpro.local:7890 \
    "$repo_dir/scripts/install-broker-user-service.sh" >/dev/null
[ "$(sed -n "s/^WSL_WIN_RELAY_ATTACH_TOKEN='\([^']*\)'$/\1/p" "$env_file")" = "$token" ]
[ "$(cat "$token_file")" = "$token" ]
grep -Fqx "WSL_WIN_RELAY_UPSTREAM_PROXY='socks5h://matebookxpro.local:7890'" "$env_file"

if HOME="$tmp_dir/home" \
    XDG_CONFIG_HOME="$tmp_dir/config" \
    PATH="$tmp_dir/bin:/usr/bin:/bin" \
    WSL_WIN_RELAY_BROKER_EXE=/bin/echo \
    WSL_WIN_RELAY_UPSTREAM_PROXY=socks5h://different.example:7890 \
        "$repo_dir/scripts/install-broker-user-service.sh" >"$tmp_dir/upstream-mismatch.out" 2>&1; then
    echo "broker installer unexpectedly accepted a conflicting upstream proxy" >&2
    exit 1
fi
grep -q 'environment upstream proxy does not match' "$tmp_dir/upstream-mismatch.out"

rm -f "$token_file"
if HOME="$tmp_dir/home" \
    XDG_CONFIG_HOME="$tmp_dir/config" \
    PATH="$tmp_dir/bin:/usr/bin:/bin" \
    WSL_WIN_RELAY_BROKER_EXE=/bin/echo \
        "$repo_dir/scripts/install-broker-user-service.sh" >"$tmp_dir/missing-token.out" 2>&1; then
    echo "broker installer unexpectedly accepted a missing configured token file" >&2
    exit 1
fi
grep -q 'token file is missing or non-regular' "$tmp_dir/missing-token.out"
printf '%s\n' "$(printf '%064x' 0)" >"$token_file"
chmod 600 "$token_file"
if HOME="$tmp_dir/home" \
    XDG_CONFIG_HOME="$tmp_dir/config" \
    PATH="$tmp_dir/bin:/usr/bin:/bin" \
    WSL_WIN_RELAY_BROKER_EXE=/bin/echo \
        "$repo_dir/scripts/install-broker-user-service.sh" >"$tmp_dir/mismatched-token.out" 2>&1; then
    echo "broker installer unexpectedly accepted a mismatched token file" >&2
    exit 1
fi
grep -q 'environment token does not match' "$tmp_dir/mismatched-token.out"

printf '%s\n' '#!/bin/sh' 'printf "%s\n" "$*" >"$WWR_TEST_PROXY_ARGS"' >"$tmp_dir/home/bin/wsl-proxy-linux"
chmod 755 "$tmp_dir/home/bin/wsl-proxy-linux"
printf '%s\n' '{}' >"$config_dir/config.json"
WWR_TEST_PROXY_ARGS="$tmp_dir/proxy.args" \
HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
    "$repo_dir/scripts/run-user-service.sh"
grep -Fq -- "-broker-mode -relay-exe /bin/true" "$tmp_dir/proxy.args"

mkdir -p "$tmp_dir/windows" "$tmp_dir/derived-home" "$tmp_dir/derived-config"
: >"$tmp_dir/windows/wsl-win-broker.exe"
: >"$tmp_dir/windows/wsl-win-connector.exe"
(
    unset WSL_WIN_RELAY_CONNECTOR_EXE
    HOME="$tmp_dir/derived-home" \
    XDG_CONFIG_HOME="$tmp_dir/derived-config" \
    PATH="$tmp_dir/bin:/usr/bin:/bin" \
    WSL_WIN_RELAY_BROKER_EXE="$tmp_dir/windows/wsl-win-broker.exe" \
        "$repo_dir/scripts/install-broker-user-service.sh" >/dev/null
)
grep -Fqx "WSL_WIN_RELAY_CONNECTOR_EXE='$tmp_dir/windows/wsl-win-connector.exe'" "$tmp_dir/derived-config/wsl-win-relay/broker.env"
echo "broker installer creates and preserves protected token file"
