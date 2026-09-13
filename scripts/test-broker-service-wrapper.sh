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

if command -v wslpath >/dev/null 2>&1; then
    windows_echo=$tmp_dir/fake.exe
    ln -s /bin/echo "$windows_echo"
    windows_env=$tmp_dir/windows.env
    write_env "$windows_env" windows-path-endpoint
    printf 'WSL_WIN_RELAY_BROKER_EXE=%s\n' "$windows_echo" >>"$windows_env"
    printf 'WSL_WIN_RELAY_ATTACH_TOKEN_FILE=%s\n' "$token_path" >>"$windows_env"
    windows_output=$(WSL_WIN_RELAY_BROKER_ENV_FILE="$windows_env" WSL_WIN_RELAY_BROKER_EXE="$windows_echo" "$repo_dir/scripts/run-broker-user-service.sh")
    windows_token_path=$(wslpath -w "$token_path")
    [ "$windows_output" = "-supervise -endpoint windows-path-endpoint -token-file $windows_token_path" ] || {
        echo "unexpected converted Windows token path: $windows_output" >&2
        exit 1
    }
fi

legacy_env=$tmp_dir/legacy.env
write_env "$legacy_env" legacy-endpoint
legacy_output=$(WSL_WIN_RELAY_BROKER_ENV_FILE="$legacy_env" "$repo_dir/scripts/run-broker-user-service.sh")
[ "$legacy_output" = "-supervise -endpoint legacy-endpoint -token-hex deadbeef" ] || {
    echo "unexpected legacy wrapper args: $legacy_output" >&2
    exit 1
}

capture_script=$tmp_dir/capture-broker.sh
capture_file=$tmp_dir/capture.env
printf '%s\n' '#!/bin/sh' 'printf "%s\\n" "$WSL_WIN_RELAY_UPSTREAM_PROXY" >"$WWR_TEST_CAPTURE"' 'printf "%s\\n" "$WSLENV" >>"$WWR_TEST_CAPTURE"' 'printf "%s" "$*"' >"$capture_script"
chmod 700 "$capture_script"
upstream_env=$tmp_dir/upstream.env
write_env "$upstream_env" upstream-endpoint
printf 'WSL_WIN_RELAY_BROKER_EXE=%s\n' "$capture_script" >>"$upstream_env"
printf '%s\n' 'WSL_WIN_RELAY_UPSTREAM_PROXY=socks5h://matebookxpro.local:7890' >>"$upstream_env"
upstream_output=$(WSLENV= WWR_TEST_CAPTURE="$capture_file" WSL_WIN_RELAY_BROKER_ENV_FILE="$upstream_env" "$repo_dir/scripts/run-broker-user-service.sh")
[ "$upstream_output" = "-supervise -endpoint upstream-endpoint -token-hex deadbeef" ] || {
    echo "unexpected upstream wrapper args: $upstream_output" >&2
    exit 1
}
grep -qx 'socks5h://matebookxpro.local:7890' "$capture_file"
tail -n 1 "$capture_file" | grep -qx 'WSL_WIN_RELAY_UPSTREAM_PROXY' || {
    echo "upstream proxy was not added to WSLENV" >&2
    exit 1
}

echo "broker service wrapper token-file and legacy credential paths passed"
