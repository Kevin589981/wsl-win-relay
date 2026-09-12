#!/bin/sh
set -eu

config_root=${XDG_CONFIG_HOME:-"$HOME/.config"}
env_file=${WSL_WIN_RELAY_BROKER_ENV_FILE:-"$config_root/wsl-win-relay/broker.env"}

if [ -L "$env_file" ] || [ ! -f "$env_file" ]; then
    echo "invalid broker environment file: $env_file" >&2
    exit 1
fi
case "$(stat -c '%a' "$env_file" 2>/dev/null || stat -f '%Lp' "$env_file")" in
    600|0600) ;;
    *) echo "broker environment file must have mode 0600: $env_file" >&2; exit 1 ;;
esac

# The file is private to the user and contains the attach token used by both
# the broker and the WSL proxy's connector children.
set -a
. "$env_file"
set +a
: "${WSL_WIN_RELAY_BROKER_EXE:?WSL_WIN_RELAY_BROKER_EXE is required in $env_file}"
: "${WSL_WIN_RELAY_ATTACH_TOKEN:?WSL_WIN_RELAY_ATTACH_TOKEN is required in $env_file}"
broker_endpoint=${WSL_WIN_RELAY_BROKER_ENDPOINT:-wsl-win-relay-broker}

if [ ! -f "$WSL_WIN_RELAY_BROKER_EXE" ] && ! command -v "$WSL_WIN_RELAY_BROKER_EXE" >/dev/null 2>&1; then
    echo "Windows broker executable is unavailable: $WSL_WIN_RELAY_BROKER_EXE" >&2
    exit 1
fi

exec "$WSL_WIN_RELAY_BROKER_EXE" -endpoint "$broker_endpoint"
