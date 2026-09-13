#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "install-transparent-service.sh requires root" >&2
    exit 1
fi

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mode=install
case "$#" in
    0) ;;
    1) [ "$1" = "--uninstall" ] && mode=uninstall || { echo "usage: install-transparent-service.sh [--uninstall]" >&2; exit 2; } ;;
    *) echo "usage: install-transparent-service.sh [--uninstall]" >&2; exit 2 ;;
esac
install_root=${WWR_INSTALL_ROOT:-}
case "$install_root" in
    ""|/*) ;;
    *) echo "WWR_INSTALL_ROOT must be an absolute path" >&2; exit 1 ;;
esac

libexec_dir=$install_root/usr/local/libexec/wsl-win-relay
config_dir=$install_root/etc/wsl-win-relay
unit_dir=$install_root/etc/systemd/system
script_target=$libexec_dir/transparent-relay.sh
binary_target=$libexec_dir/tun2socks
config_target=$config_dir/transparent.env
unit_target=$unit_dir/wsl-win-relay-transparent.service

for target in "$script_target" "$binary_target" "$unit_target"; do
    if [ -L "$target" ]; then
        echo "refusing symlinked installation target: $target" >&2
        exit 1
    fi
done

if [ "$mode" = uninstall ]; then
    if [ -e "$unit_target" ]; then
        systemctl disable --now wsl-win-relay-transparent.service
    else
        systemctl disable --now wsl-win-relay-transparent.service >/dev/null 2>&1 || true
    fi
    rm -f "$unit_target" "$script_target" "$binary_target"
    systemctl daemon-reload
    echo "removed wsl-win-relay-transparent.service; preserved $config_target"
    exit 0
fi

if [ -L "$config_target" ]; then
    echo "refusing symlinked installation target: $config_target" >&2
    exit 1
fi
tun2socks_bin=${WWR_TUN2SOCKS_BIN:-$(command -v tun2socks 2>/dev/null || true)}
if [ -z "$tun2socks_bin" ] || [ ! -x "$tun2socks_bin" ]; then
    echo "tun2socks is required; run scripts/install-tun2socks.sh or set WWR_TUN2SOCKS_BIN" >&2
    exit 1
fi

mkdir -p "$libexec_dir" "$config_dir" "$unit_dir"
chmod 700 "$config_dir"
install -m 0755 "$repo_dir/scripts/transparent-relay.sh" "$script_target"
install -m 0755 "$tun2socks_bin" "$binary_target"
install -m 0644 "$repo_dir/systemd/wsl-win-relay-transparent.service" "$unit_target"
if [ ! -e "$config_target" ]; then
    install -m 0600 "$repo_dir/systemd/wsl-win-relay-transparent.env" "$config_target"
    echo "created $config_target; review DNS and uplink settings before use" >&2
fi
if [ ! -f "$config_target" ]; then
    echo "refusing non-regular transparent configuration: $config_target" >&2
    exit 1
fi
chmod 600 "$config_target"

systemctl daemon-reload
systemctl enable wsl-win-relay-transparent.service
systemctl restart wsl-win-relay-transparent.service
echo "enabled and restarted wsl-win-relay-transparent.service"
