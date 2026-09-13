#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
bin_dir=$HOME/bin
service_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/systemd/user
config_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/wsl-win-relay
env_file=$config_dir/broker.env
token_file=$config_dir/attach.token
requested_upstream_proxy=${WSL_WIN_RELAY_UPSTREAM_PROXY:-}
requested_connector_exe=${WSL_WIN_RELAY_CONNECTOR_EXE:-}

quote_env_value() {
    value=$1
    value=$(printf '%s' "$value" | sed "s/'/'\\\\''/g")
    printf "'%s'" "$value"
}

if [ -z "${WSL_WIN_RELAY_BROKER_EXE:-}" ]; then
    echo "set WSL_WIN_RELAY_BROKER_EXE to the mounted Windows broker executable" >&2
    exit 2
fi
if [ ! -f "$WSL_WIN_RELAY_BROKER_EXE" ] && ! command -v "$WSL_WIN_RELAY_BROKER_EXE" >/dev/null 2>&1; then
    echo "Windows broker executable is unavailable: $WSL_WIN_RELAY_BROKER_EXE" >&2
    exit 1
fi
broker_exe_path=$WSL_WIN_RELAY_BROKER_EXE
if [ ! -f "$broker_exe_path" ]; then
    broker_exe_path=$(command -v "$broker_exe_path")
fi
if [ -z "$requested_connector_exe" ]; then
    requested_connector_exe=$(dirname "$broker_exe_path")/wsl-win-connector.exe
fi
if [ ! -f "$requested_connector_exe" ] && ! command -v "$requested_connector_exe" >/dev/null 2>&1; then
    echo "Windows connector executable is unavailable: $requested_connector_exe (set WSL_WIN_RELAY_CONNECTOR_EXE)" >&2
    exit 1
fi
if [ -L "$env_file" ] || { [ -e "$env_file" ] && [ ! -f "$env_file" ]; }; then
    echo "refusing non-regular broker environment file: $env_file" >&2
    exit 1
fi
if [ -L "$token_file" ] || { [ -e "$token_file" ] && [ ! -f "$token_file" ]; }; then
    echo "refusing non-regular broker token file: $token_file" >&2
    exit 1
fi

mkdir -p "$bin_dir" "$service_dir" "$config_dir"
chmod 700 "$config_dir"
install -m 0755 "$repo_dir/scripts/run-broker-user-service.sh" "$bin_dir/wsl-win-relay-broker-service"
install -m 0644 "$repo_dir/systemd/wsl-win-relay-broker.service" "$service_dir/wsl-win-relay-broker.service"

validate_token_file() {
    token_path=$1
    if [ -L "$token_path" ] || [ ! -f "$token_path" ]; then
        echo "configured broker token file is missing or non-regular: $token_path" >&2
        return 1
    fi
    chmod 600 "$token_path"
    token_value=$(tr -d ' \t\r\n' < "$token_path")
    token_length=${#token_value}
    case "$token_value" in
        ''|*[!0-9A-Fa-f]*)
            echo "configured broker token file is not hexadecimal: $token_path" >&2
            return 1
            ;;
    esac
    if [ $((token_length % 2)) -ne 0 ]; then
        echo "configured broker token file has an odd-length hexadecimal token: $token_path" >&2
        return 1
    fi
    configured_token=$(sed -n "s/^WSL_WIN_RELAY_ATTACH_TOKEN='\([0-9A-Fa-f]*\)'$/\1/p" "$env_file" | head -n 1)
    if [ -n "$configured_token" ] && [ "$configured_token" != "$token_value" ]; then
        echo "broker environment token does not match $token_path" >&2
        return 1
    fi
}

if [ -f "$env_file" ]; then
    configured_token_file=$(sed -n "s/^WSL_WIN_RELAY_ATTACH_TOKEN_FILE='\(.*\)'$/\1/p" "$env_file" | head -n 1)
    if [ -n "$configured_token_file" ]; then
        token_file=$configured_token_file
        validate_token_file "$token_file"
    fi
fi

if [ ! -e "$env_file" ]; then
    token=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
    endpoint=${WSL_WIN_RELAY_BROKER_ENDPOINT:-wsl-win-relay-broker}
    umask 077
    printf '%s\n' "$token" >"$token_file"
    {
        printf '%s\n' '# Private broker settings; keep mode 0600.'
        printf 'WSL_WIN_RELAY_BROKER_EXE=%s\n' "$(quote_env_value "$WSL_WIN_RELAY_BROKER_EXE")"
        printf 'WSL_WIN_RELAY_CONNECTOR_EXE=%s\n' "$(quote_env_value "$requested_connector_exe")"
        printf 'WSL_WIN_RELAY_BROKER_ENDPOINT=%s\n' "$(quote_env_value "$endpoint")"
        printf 'WSL_WIN_RELAY_ATTACH_TOKEN=%s\n' "$(quote_env_value "$token")"
        printf 'WSL_WIN_RELAY_ATTACH_TOKEN_FILE=%s\n' "$(quote_env_value "$token_file")"
        printf 'WSL_WIN_RELAY_BROKER_MODE=1\n'
        if [ -n "${WSL_WIN_RELAY_UPSTREAM_PROXY:-}" ]; then
            printf 'WSL_WIN_RELAY_UPSTREAM_PROXY=%s\n' "$(quote_env_value "$WSL_WIN_RELAY_UPSTREAM_PROXY")"
        fi
    } >"$env_file"
fi
configured_connector_exe=$(sed -n "s/^WSL_WIN_RELAY_CONNECTOR_EXE='\(.*\)'$/\1/p" "$env_file" | head -n 1)
if [ -n "$configured_connector_exe" ] && [ "$configured_connector_exe" != "$requested_connector_exe" ]; then
    echo "broker environment connector executable does not match the requested value" >&2
    exit 1
fi
if [ -z "$configured_connector_exe" ]; then
    printf 'WSL_WIN_RELAY_CONNECTOR_EXE=%s\n' "$(quote_env_value "$requested_connector_exe")" >>"$env_file"
fi
if ! grep -q '^WSL_WIN_RELAY_BROKER_MODE=' "$env_file"; then
    printf '%s\n' 'WSL_WIN_RELAY_BROKER_MODE=1' >>"$env_file"
fi
if [ -n "$requested_upstream_proxy" ]; then
    configured_upstream_proxy=$(sed -n "s/^WSL_WIN_RELAY_UPSTREAM_PROXY='\(.*\)'$/\1/p" "$env_file" | head -n 1)
    if [ -n "$configured_upstream_proxy" ] && [ "$configured_upstream_proxy" != "$requested_upstream_proxy" ]; then
        echo "broker environment upstream proxy does not match the requested value" >&2
        exit 1
    fi
    if [ -z "$configured_upstream_proxy" ]; then
        printf 'WSL_WIN_RELAY_UPSTREAM_PROXY=%s\n' "$(quote_env_value "$requested_upstream_proxy")" >>"$env_file"
    fi
fi
chmod 600 "$env_file"
if [ -f "$token_file" ]; then
    validate_token_file "$token_file"
fi

systemctl --user daemon-reload
systemctl --user enable wsl-win-relay-broker.service
systemctl --user restart wsl-win-relay-broker.service
echo "enabled and restarted wsl-win-relay-broker.service"
echo "broker mode is now the default for the proxy service when this env file is loaded"
echo "restart wsl-win-relay.service to attach it to the broker"
