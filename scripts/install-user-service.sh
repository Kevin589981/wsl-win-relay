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
if [ -r "$repo_dir/lib/libwsl_win_relay_listen.so" ]; then
    mkdir -p "$lib_dir"
    install -m 0755 "$repo_dir/lib/libwsl_win_relay_listen.so" "$lib_dir/libwsl_win_relay_listen.so"
fi
mkdir -p "$service_dir" "$config_dir"
install -m 0644 "$repo_dir/systemd/wsl-win-relay.service" "$service_dir/wsl-win-relay.service"
if [ ! -e "$config_dir/config.json" ]; then
    install -m 0600 "$repo_dir/wsl-win-relay.example.json" "$config_dir/config.json"
    echo "created $config_dir/config.json; edit relay_exe before starting" >&2
fi
if [ -L "$config_dir/config.json" ] || [ ! -f "$config_dir/config.json" ]; then
    echo "refusing non-regular config path: $config_dir/config.json" >&2
    exit 1
fi
chmod 600 "$config_dir/config.json"
systemctl --user daemon-reload
systemctl --user enable --now wsl-win-relay.service
echo "enabled wsl-win-relay.service"
