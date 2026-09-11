#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "transparent-relay.sh requires root for TUN and route changes" >&2
    exit 1
fi

command -v ip >/dev/null 2>&1 || { echo "ip command is required" >&2; exit 1; }
command -v tun2socks >/dev/null 2>&1 || { echo "tun2socks is required (run scripts/install-tun2socks.sh)" >&2; exit 1; }

device=${WWR_TUN_DEVICE:-tun0}
tun_address=${WWR_TUN_ADDRESS:-198.18.0.1/15}
proxy=${WWR_TUN_PROXY:-socks5://127.0.0.1:1080}
uplink=${WWR_UPLINK_INTERFACE:-}
dns=${WWR_DNS:-}
route_added=0
route6_added=0
tun_added=0
dns_backup=
dns_was_present=0
dns_symlink=0
dns_link_target=
tun_pid=

cleanup() {
    trap - EXIT INT TERM
    if [ -n "${tun_pid:-}" ]; then
        kill "$tun_pid" 2>/dev/null || true
        wait "$tun_pid" 2>/dev/null || true
    fi
    if [ "$route_added" -eq 1 ]; then
        ip route del 0.0.0.0/1 dev "$device" 2>/dev/null || true
        ip route del 128.0.0.0/1 dev "$device" 2>/dev/null || true
    fi
    if [ "$route6_added" -eq 1 ]; then
        ip -6 route del ::/1 dev "$device" 2>/dev/null || true
        ip -6 route del 8000::/1 dev "$device" 2>/dev/null || true
    fi
    if [ -n "$dns_backup" ]; then
        if [ "$dns_symlink" -eq 1 ]; then
            rm -f /etc/resolv.conf
            ln -s "$dns_link_target" /etc/resolv.conf
        elif [ "$dns_was_present" -eq 1 ]; then
            cat "$dns_backup" > /etc/resolv.conf
        else
            rm -f /etc/resolv.conf
        fi
        rm -f "$dns_backup"
    fi
    if [ "$tun_added" -eq 1 ]; then
        ip link del "$device" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM

if [ -z "$uplink" ]; then
    uplink=$(ip route show default | awk 'NR==1 {print $5}')
fi
if [ -z "$uplink" ]; then
    echo "unable to determine uplink interface; set WWR_UPLINK_INTERFACE" >&2
    exit 1
fi

if ip link show "$device" >/dev/null 2>&1; then
    echo "refusing to reuse existing TUN device $device" >&2
    exit 1
fi
ip tuntap add mode tun dev "$device"
tun_added=1
ip addr add "$tun_address" dev "$device"
ip link set dev "$device" up

if [ -n "$dns" ]; then
    dns_backup=$(mktemp /tmp/wsl-win-relay-resolv.XXXXXX)
    if [ -L /etc/resolv.conf ]; then
        dns_symlink=1
        dns_link_target=$(readlink /etc/resolv.conf)
        if [ -e /etc/resolv.conf ]; then cat /etc/resolv.conf > "$dns_backup"; dns_was_present=1; fi
        rm -f /etc/resolv.conf
    elif [ -e /etc/resolv.conf ]; then
        cat /etc/resolv.conf > "$dns_backup"
        dns_was_present=1
    fi
    printf 'nameserver %s\n' "$dns" > /etc/resolv.conf
fi

ip route add 0.0.0.0/1 dev "$device" metric 1
route_added=1
ip route add 128.0.0.0/1 dev "$device" metric 1
if ip -6 route add ::/1 dev "$device" metric 1 2>/dev/null; then
    route6_added=1
    ip -6 route add 8000::/1 dev "$device" metric 1
fi

echo "transparent relay active: $device -> $proxy via $uplink"
tun2socks --device "$device" --proxy "$proxy" --interface "$uplink" &
tun_pid=$!
wait "$tun_pid"
