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

broker_token_path() {
    token_path=$1
    case "$WSL_WIN_RELAY_BROKER_EXE" in
        *.exe|*.EXE|/mnt/*)
            # WSL paths such as /home/... are not valid Win32 paths. Convert
            # the private token path before invoking a Windows broker binary.
            case "$token_path" in
                [A-Za-z]:\\*|\\\\*) printf '%s\n' "$token_path"; return 0 ;;
            esac
            if ! command -v wslpath >/dev/null 2>&1; then
                echo "wslpath is required to pass a WSL token file to Windows" >&2
                exit 1
            fi
            converted=$(wslpath -w "$token_path") || {
                echo "cannot convert broker token file path for Windows: $token_path" >&2
                exit 1
            }
            [ -n "$converted" ] || {
                echo "empty Windows path for broker token file: $token_path" >&2
                exit 1
            }
            printf '%s\n' "$converted"
            ;;
        *) printf '%s\n' "$token_path" ;;
    esac
}

if [ -n "${WSL_WIN_RELAY_ATTACH_TOKEN_FILE:-}" ]; then
    token_file=$WSL_WIN_RELAY_ATTACH_TOKEN_FILE
    if [ -L "$token_file" ] || [ ! -f "$token_file" ]; then
        echo "invalid broker token file: $token_file" >&2
        exit 1
    fi
    case "$(stat -c '%a' "$token_file" 2>/dev/null || stat -f '%Lp' "$token_file")" in
        600|0600) ;;
        *) echo "broker token file must have mode 0600: $token_file" >&2; exit 1 ;;
    esac
    broker_token_file=$(broker_token_path "$token_file")
fi

# Older installations only have the private environment token. Keep the
# `-token-hex` fallback below so upgrading the wrapper does not invalidate
# their service.

if [ ! -f "$WSL_WIN_RELAY_BROKER_EXE" ] && ! command -v "$WSL_WIN_RELAY_BROKER_EXE" >/dev/null 2>&1; then
    echo "Windows broker executable is unavailable: $WSL_WIN_RELAY_BROKER_EXE" >&2
    exit 1
fi

# Keep a Windows-side parent alive so a frontend crash is recoverable without
# relying on the WSL service manager to notice the child process boundary.
if [ -n "${WSL_WIN_RELAY_ATTACH_TOKEN_FILE:-}" ]; then
    exec "$WSL_WIN_RELAY_BROKER_EXE" -supervise -endpoint "$broker_endpoint" -token-file "$broker_token_file"
fi
exec "$WSL_WIN_RELAY_BROKER_EXE" -supervise -endpoint "$broker_endpoint" -token-hex "$WSL_WIN_RELAY_ATTACH_TOKEN"
