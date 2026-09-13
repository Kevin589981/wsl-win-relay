#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
bin_dir=$HOME/bin
lib_dir=$HOME/lib
service_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/systemd/user
config_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/wsl-win-relay
if [ ! -x "$repo_dir/bin/wsl-proxy-linux" ]; then
    echo "missing $repo_dir/bin/wsl-proxy-linux; run scripts/build-wsl.sh first" >&2
    exit 1
fi
mkdir -p "$bin_dir"
install -m 0755 "$repo_dir/bin/wsl-proxy-linux" "$bin_dir/wsl-proxy-linux"
install -m 0755 "$repo_dir/scripts/wsl-win-relay-run" "$bin_dir/wsl-win-relay-run"
install -m 0755 "$repo_dir/scripts/run-user-service.sh" "$bin_dir/wsl-win-relay-service"
install -m 0755 "$repo_dir/scripts/run-broker-user-service.sh" "$bin_dir/wsl-win-relay-broker-service"
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
systemctl --user daemon-reload
if [ -n "${XDG_CONFIG_HOME:-}" ]; then
    systemctl --user import-environment XDG_CONFIG_HOME
else
    systemctl --user unset-environment XDG_CONFIG_HOME
fi
systemctl --user enable wsl-win-relay.service
systemctl --user restart wsl-win-relay.service
echo "enabled and restarted wsl-win-relay.service"
