#!/bin/sh
set -eu

config_root=${XDG_CONFIG_HOME:-"$HOME/.config"}
config="$config_root/wsl-win-relay/config.json"
binary="$HOME/bin/wsl-proxy-linux"

if [ ! -x "$binary" ]; then
    echo "missing proxy binary: $binary" >&2
    exit 1
fi
if [ -L "$config" ] || [ ! -f "$config" ]; then
    echo "invalid relay config path: $config" >&2
    exit 1
fi

exec "$binary" -config "$config"
