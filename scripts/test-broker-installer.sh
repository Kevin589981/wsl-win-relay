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
grep -Fqx "WSL_WIN_RELAY_UPSTREAM_PROXY='socks5h://matebookxpro.local:7890'" "$env_file"

HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/config" \
PATH="$tmp_dir/bin:/usr/bin:/bin" \
WSL_WIN_RELAY_BROKER_EXE=/bin/echo \
    "$repo_dir/scripts/install-broker-user-service.sh" >/dev/null
[ "$(sed -n "s/^WSL_WIN_RELAY_ATTACH_TOKEN='\([^']*\)'$/\1/p" "$env_file")" = "$token" ]
[ "$(cat "$token_file")" = "$token" ]

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
echo "broker installer creates and preserves protected token file"
