#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
bin_dir=$HOME/bin
lib_dir=$HOME/lib
service_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/systemd/user
config_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/wsl-win-relay
broker_env=$config_dir/broker.env
if [ ! -x "$repo_dir/bin/wsl-proxy-linux" ]; then
    echo "missing $repo_dir/bin/wsl-proxy-linux; run scripts/build-wsl.sh first" >&2
    exit 1
fi
if [ ! -x "$repo_dir/bin/wsl-win-relay-status" ]; then
    echo "missing $repo_dir/bin/wsl-win-relay-status; run scripts/build-wsl.sh first" >&2
    exit 1
fi
mkdir -p "$bin_dir"
install -m 0755 "$repo_dir/bin/wsl-proxy-linux" "$bin_dir/wsl-proxy-linux"
install -m 0755 "$repo_dir/bin/wsl-win-relay-status" "$bin_dir/wsl-win-relay-status"
install -m 0755 "$repo_dir/scripts/wsl-win-relay-run" "$bin_dir/wsl-win-relay-run"
install -m 0755 "$repo_dir/scripts/wsl-win-relay-shell" "$bin_dir/wsl-win-relay-shell"
install -m 0755 "$repo_dir/scripts/run-user-service.sh" "$bin_dir/wsl-win-relay-service"
install -m 0755 "$repo_dir/scripts/run-broker-user-service.sh" "$bin_dir/wsl-win-relay-broker-service"
install -m 0755 "$repo_dir/scripts/wsl-win-relay-doctor" "$bin_dir/wsl-win-relay-doctor"
if [ -x "$repo_dir/bin/wsl-win-relay-strict" ]; then
    install -m 0755 "$repo_dir/bin/wsl-win-relay-strict" "$bin_dir/wsl-win-relay-strict"
fi
if [ -r "$repo_dir/lib/libwsl_win_relay_listen.so" ]; then
    mkdir -p "$lib_dir"
    install -m 0755 "$repo_dir/lib/libwsl_win_relay_listen.so" "$lib_dir/libwsl_win_relay_listen.so"
fi
mkdir -p "$service_dir" "$config_dir"
chmod 700 "$config_dir"
install -m 0644 "$repo_dir/systemd/wsl-win-relay.service" "$service_dir/wsl-win-relay.service"
install -m 0644 "$repo_dir/systemd/wsl-win-relay-broker.service" "$service_dir/wsl-win-relay-broker.service"
config_path=$config_dir/config.json
if [ -L "$config_path" ]; then
    echo "refusing symlinked config path: $config_path" >&2
    exit 1
fi
if [ ! -e "$config_path" ]; then
    install -m 0600 "$repo_dir/wsl-win-relay.example.json" "$config_dir/config.json"
    echo "created $config_dir/config.json; edit relay_exe before starting" >&2
fi
if [ ! -f "$config_path" ]; then
    echo "refusing non-regular config path: $config_path" >&2
    exit 1
fi
chmod 600 "$config_path"
if ! "$bin_dir/wsl-proxy-linux" -config "$config_path" -check-config >/dev/null; then
    echo "relay configuration validation failed; services were not restarted" >&2
    exit 1
fi
configuration_ready=1
if grep -Eq '"relay_exe"[[:space:]]*:[[:space:]]*"/mnt/c/Users/you/bin/wsl-win-relay\.exe"' "$config_path"; then
    configuration_ready=0
    if [ -f "$broker_env" ] && [ ! -L "$broker_env" ]; then
        case "$(stat -c '%a' "$broker_env" 2>/dev/null || stat -f '%Lp' "$broker_env")" in
            600|0600)
                if grep -Eq "^WSL_WIN_RELAY_BROKER_MODE=('1'|1)$" "$broker_env"; then
                    configured_connector=$(sed -n "s/^WSL_WIN_RELAY_CONNECTOR_EXE='\(.*\)'$/\1/p" "$broker_env" | head -n 1)
                    if [ -n "$configured_connector" ] && { [ -f "$configured_connector" ] || command -v "$configured_connector" >/dev/null 2>&1; }; then
                        configuration_ready=1
                    fi
                fi
                ;;
        esac
    fi
fi
systemctl --user daemon-reload
if [ -n "${XDG_CONFIG_HOME:-}" ]; then
    systemctl --user import-environment XDG_CONFIG_HOME
else
    systemctl --user unset-environment XDG_CONFIG_HOME
fi
if [ "$configuration_ready" -eq 0 ]; then
    echo "installed wsl-win-relay.service without starting it; configure relay_exe or install broker mode, then rerun this installer"
    exit 0
fi
systemctl --user enable wsl-win-relay.service
systemctl --user restart wsl-win-relay.service
echo "enabled and restarted wsl-win-relay.service"
