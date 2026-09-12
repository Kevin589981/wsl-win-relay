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

# Broker mode keeps its token outside the JSON config. Loading this optional
# private env file also makes connector children inherit the same credentials.
broker_env=${WSL_WIN_RELAY_BROKER_ENV_FILE:-"$config_root/wsl-win-relay/broker.env"}
if [ -f "$broker_env" ] && [ ! -L "$broker_env" ]; then
    case "$(stat -c '%a' "$broker_env" 2>/dev/null || stat -f '%Lp' "$broker_env")" in
        600|0600) ;;
        *) echo "broker environment file must have mode 0600: $broker_env" >&2; exit 1 ;;
    esac
    set -a
    . "$broker_env"
    set +a
fi

exec "$binary" -config "$config"
