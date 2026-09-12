#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

write_env() {
    env_path=$1
    endpoint=$2
    {
        printf '%s\n' 'WSL_WIN_RELAY_BROKER_EXE=/bin/echo'
        printf 'WSL_WIN_RELAY_BROKER_ENDPOINT=%s\n' "$endpoint"
        printf '%s\n' 'WSL_WIN_RELAY_ATTACH_TOKEN=deadbeef'
    } >"$env_path"
    chmod 600 "$env_path"
}

token_path=$tmp_dir/attach.token
env_path=$tmp_dir/broker.env
printf '%s\n' deadbeef >"$token_path"
chmod 600 "$token_path"
write_env "$env_path" token-file-endpoint
printf 'WSL_WIN_RELAY_ATTACH_TOKEN_FILE=%s\n' "$token_path" >>"$env_path"
token_output=$(WSL_WIN_RELAY_BROKER_ENV_FILE="$env_path" "$repo_dir/scripts/run-broker-user-service.sh")
[ "$token_output" = "-supervise -endpoint token-file-endpoint -token-file $token_path" ] || {
    echo "unexpected token-file wrapper args: $token_output" >&2
    exit 1
}

legacy_env=$tmp_dir/legacy.env
write_env "$legacy_env" legacy-endpoint
legacy_output=$(WSL_WIN_RELAY_BROKER_ENV_FILE="$legacy_env" "$repo_dir/scripts/run-broker-user-service.sh")
[ "$legacy_output" = "-supervise -endpoint legacy-endpoint -token-hex deadbeef" ] || {
    echo "unexpected legacy wrapper args: $legacy_output" >&2
    exit 1
}

echo "broker service wrapper token-file and legacy credential paths passed"
