#!/bin/sh
set -eu

mode=install
case "$#" in
    0) ;;
    1) [ "$1" = "--uninstall" ] && mode=uninstall || { echo "usage: install-user-service.sh [--uninstall]" >&2; exit 2; } ;;
    *) echo "usage: install-user-service.sh [--uninstall]" >&2; exit 2 ;;
esac
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
bin_dir=$HOME/bin
lib_dir=$HOME/lib
service_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/systemd/user
config_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/wsl-win-relay
broker_env=$config_dir/broker.env
proxy_binary=$bin_dir/wsl-proxy-linux
status_binary=$bin_dir/wsl-win-relay-status
run_wrapper=$bin_dir/wsl-win-relay-run
shell_wrapper=$bin_dir/wsl-win-relay-shell
proxy_wrapper=$bin_dir/wsl-win-relay-service
broker_wrapper=$bin_dir/wsl-win-relay-broker-service
doctor=$bin_dir/wsl-win-relay-doctor
strict_binary=$bin_dir/wsl-win-relay-strict
interposer=$lib_dir/libwsl_win_relay_listen.so
proxy_unit=$service_dir/wsl-win-relay.service
broker_unit=$service_dir/wsl-win-relay-broker.service

for target in "$proxy_binary" "$status_binary" "$run_wrapper" "$shell_wrapper" "$proxy_wrapper" "$broker_wrapper" "$doctor" "$strict_binary" "$interposer" "$proxy_unit" "$broker_unit"; do
    if [ -L "$target" ]; then
        echo "refusing symlinked installation target: $target" >&2
        exit 1
    fi
done

if [ "$mode" = uninstall ]; then
    if [ -e "$proxy_unit" ]; then
        systemctl --user disable --now wsl-win-relay.service
    else
        systemctl --user disable --now wsl-win-relay.service >/dev/null 2>&1 || true
    fi
    if [ -e "$broker_unit" ]; then
        systemctl --user disable --now wsl-win-relay-broker.service
    else
        systemctl --user disable --now wsl-win-relay-broker.service >/dev/null 2>&1 || true
    fi
    rm -f "$proxy_unit" "$broker_unit" "$proxy_binary" "$status_binary" "$run_wrapper" "$shell_wrapper" "$proxy_wrapper" "$broker_wrapper" "$doctor" "$strict_binary" "$interposer"
    systemctl --user daemon-reload
    echo "removed wsl-win-relay user services and deployed binaries; preserved $config_dir"
    exit 0
fi

if [ ! -x "$repo_dir/bin/wsl-proxy-linux" ]; then
    echo "missing $repo_dir/bin/wsl-proxy-linux; run scripts/build-wsl.sh first" >&2
    exit 1
fi
if [ ! -x "$repo_dir/bin/wsl-win-relay-status" ]; then
    echo "missing $repo_dir/bin/wsl-win-relay-status; run scripts/build-wsl.sh first" >&2
    exit 1
fi
mkdir -p "$bin_dir"
install -m 0755 "$repo_dir/bin/wsl-proxy-linux" "$proxy_binary"
install -m 0755 "$repo_dir/bin/wsl-win-relay-status" "$status_binary"
install -m 0755 "$repo_dir/scripts/wsl-win-relay-run" "$run_wrapper"
install -m 0755 "$repo_dir/scripts/wsl-win-relay-shell" "$shell_wrapper"
install -m 0755 "$repo_dir/scripts/run-user-service.sh" "$proxy_wrapper"
install -m 0755 "$repo_dir/scripts/run-broker-user-service.sh" "$broker_wrapper"
install -m 0755 "$repo_dir/scripts/wsl-win-relay-doctor" "$doctor"
if [ -x "$repo_dir/bin/wsl-win-relay-strict" ]; then
    install -m 0755 "$repo_dir/bin/wsl-win-relay-strict" "$strict_binary"
fi
if [ -r "$repo_dir/lib/libwsl_win_relay_listen.so" ]; then
    mkdir -p "$lib_dir"
    install -m 0755 "$repo_dir/lib/libwsl_win_relay_listen.so" "$interposer"
fi
mkdir -p "$service_dir" "$config_dir"
chmod 700 "$config_dir"
install -m 0644 "$repo_dir/systemd/wsl-win-relay.service" "$proxy_unit"
install -m 0644 "$repo_dir/systemd/wsl-win-relay-broker.service" "$broker_unit"
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
