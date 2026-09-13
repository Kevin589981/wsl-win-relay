#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "transparent-relay.sh requires root for TUN and route changes" >&2
    exit 1
fi

command -v ip >/dev/null 2>&1 || { echo "ip command is required" >&2; exit 1; }

tun2socks_bin=${WWR_TUN2SOCKS_BIN:-}
if [ -z "$tun2socks_bin" ]; then
    tun2socks_bin=$(command -v tun2socks 2>/dev/null || true)
fi
if [ -z "$tun2socks_bin" ] && command -v go >/dev/null 2>&1; then
    gopath=$(go env GOPATH 2>/dev/null || true)
    if [ -n "$gopath" ] && [ -x "$gopath/bin/tun2socks" ]; then
        tun2socks_bin="$gopath/bin/tun2socks"
    fi
fi
if [ -z "$tun2socks_bin" ] || [ ! -x "$tun2socks_bin" ]; then
    echo "tun2socks is required (run scripts/install-tun2socks.sh or set WWR_TUN2SOCKS_BIN)" >&2
    exit 1
fi

device=${WWR_TUN_DEVICE:-tun0}
tun_address=${WWR_TUN_ADDRESS:-198.18.0.1/15}
proxy=${WWR_TUN_PROXY:-socks5://127.0.0.1:1080}
uplink=${WWR_UPLINK_INTERFACE:-}
dns=${WWR_DNS:-}
resolv_conf=${WWR_RESOLV_CONF:-/etc/resolv.conf}
proxy_wait_seconds=${WWR_TUN_PROXY_WAIT_SECONDS:-30}
proc_net_tcp=${WWR_PROC_NET_TCP:-/proc/net/tcp}
proc_net_tcp6=${WWR_PROC_NET_TCP6:-/proc/net/tcp6}
case "$proxy_wait_seconds" in
    ''|*[!0-9]*) echo "WWR_TUN_PROXY_WAIT_SECONDS must be an integer" >&2; exit 1 ;;
esac
if [ "$proxy_wait_seconds" -gt 300 ]; then
    proxy_wait_seconds=300
fi
uplink_fallback=0
route_added=0
route6_first_added=0
route6_second_added=0
tun_added=0
dns_backup=
dns_was_present=0
dns_symlink=0
dns_link_target=
tun_pid=
proxy_route_file=

cleanup() {
    trap - EXIT INT TERM HUP QUIT
    if [ -n "${tun_pid:-}" ]; then
        kill "$tun_pid" 2>/dev/null || true
        for _ in 1 2 3 4 5; do
            if ! kill -0 "$tun_pid" 2>/dev/null; then
                break
            fi
            sleep 0.1
        done
        if kill -0 "$tun_pid" 2>/dev/null; then
            kill -KILL "$tun_pid" 2>/dev/null || true
        fi
        wait "$tun_pid" 2>/dev/null || true
    fi
    if [ "$route_added" -eq 1 ]; then
        ip route del 0.0.0.0/1 dev "$device" 2>/dev/null || true
        ip route del 128.0.0.0/1 dev "$device" 2>/dev/null || true
    fi
    if [ "$route6_first_added" -eq 1 ]; then
        ip -6 route del ::/1 dev "$device" 2>/dev/null || true
    fi
    if [ "$route6_second_added" -eq 1 ]; then
        ip -6 route del 8000::/1 dev "$device" 2>/dev/null || true
    fi
    if [ -n "${proxy_route_file:-}" ] && [ -f "$proxy_route_file" ]; then
        while read -r family address; do
            [ -n "$family" ] || continue
            if [ "$family" = 6 ]; then
                ip -6 route del "$address/128" 2>/dev/null || true
            else
                ip route del "$address/32" 2>/dev/null || true
            fi
        done < "$proxy_route_file"
        rm -f "$proxy_route_file"
    fi
    if [ -n "$dns_backup" ]; then
        if [ "$dns_symlink" -eq 1 ]; then
            rm -f "$resolv_conf"
            ln -s "$dns_link_target" "$resolv_conf"
        elif [ "$dns_was_present" -eq 1 ]; then
            cat "$dns_backup" > "$resolv_conf"
        else
            rm -f "$resolv_conf"
        fi
        rm -f "$dns_backup"
    fi
    if [ "$tun_added" -eq 1 ]; then
        ip link del "$device" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM HUP QUIT

if [ -z "$uplink" ]; then
    uplink=$(ip route show default | awk 'NR==1 {print $5}')
fi
if [ -z "$uplink" ]; then
    # When HNS has removed WSL's default interface, a local SOCKS relay is
    # still reachable through loopback. Keep the emergency path usable without
    # requiring an interface override in that specific case.
    case "$proxy" in
        socks5://127.0.0.1:*|socks5h://127.0.0.1:*|socks5://localhost:*|socks5h://localhost:*|socks5://\[::1\]:*|socks5h://\[::1\]:*)
            if ip link show lo >/dev/null 2>&1; then
                uplink=lo
                uplink_fallback=1
            fi
            ;;
    esac
fi
if [ -z "$uplink" ]; then
    echo "unable to determine uplink interface; set WWR_UPLINK_INTERFACE" >&2
    exit 1
fi

proxy_host=
proxy_port=
proxy_authority=${proxy#*://}
proxy_authority=${proxy_authority%%/*}
proxy_authority=${proxy_authority%%\?*}
proxy_authority=${proxy_authority##*@}
case "$proxy_authority" in
    \[*\]:*) proxy_host=${proxy_authority#\[}; proxy_host=${proxy_host%%\]*}; proxy_port=${proxy_authority##*:} ;;
    *:*) proxy_host=${proxy_authority%:*}; proxy_port=${proxy_authority##*:} ;;
    *) proxy_host=$proxy_authority ;;
esac

wait_for_local_proxy() {
    case "$proxy_host" in
        localhost|127.*|0.0.0.0|::1|::) ;;
        *) return 0 ;;
    esac
    case "$proxy_port" in
        ''|*[!0-9]*) echo "local TUN proxy must include a numeric port: $proxy" >&2; return 1 ;;
    esac
    if [ "$proxy_port" -lt 1 ] || [ "$proxy_port" -gt 65535 ]; then
        echo "local TUN proxy port is outside 1..65535: $proxy_port" >&2
        return 1
    fi
    port_hex=$(printf '%04X' "$proxy_port")
    attempts=$((proxy_wait_seconds * 10 + 1))
    for _ in $(seq 1 "$attempts"); do
        if { [ -r "$proc_net_tcp" ] && awk -v suffix=":$port_hex" '$4 == "0A" && substr($2, length($2) - length(suffix) + 1) == suffix { found=1 } END { exit !found }' "$proc_net_tcp"; } ||
           { [ -r "$proc_net_tcp6" ] && awk -v suffix=":$port_hex" '$4 == "0A" && substr($2, length($2) - length(suffix) + 1) == suffix { found=1 } END { exit !found }' "$proc_net_tcp6"; }; then
            return 0
        fi
        [ "$proxy_wait_seconds" -gt 0 ] || break
        sleep 0.1
    done
    echo "local TUN proxy did not start within ${proxy_wait_seconds}s: $proxy" >&2
    return 1
}

wait_for_local_proxy

proxy_addresses=
case "$proxy_host" in
    ""|localhost|127.*|::1)
        ;;
    *:*)
        proxy_addresses=$proxy_host
        ;;
    *[!0-9.]*|*[!0-9])
        if ! command -v getent >/dev/null 2>&1; then
            echo "getent is required to resolve the non-loopback TUN proxy $proxy_host" >&2
            exit 1
        fi
        proxy_addresses=$(getent ahosts "$proxy_host" | awk '{print $1}' | sort -u)
        ;;
    *)
        proxy_addresses=$proxy_host
        ;;
esac
if [ -n "$proxy_host" ] && [ -n "$proxy_addresses" ]; then
    proxy_route_file=$(mktemp /tmp/wsl-win-relay-proxy-route.XXXXXX)
    while read -r proxy_address; do
        [ -n "$proxy_address" ] || continue
        route_family=4
        route_prefix=32
        route_line=$(ip route get "$proxy_address" 2>/dev/null | awk 'NR==1 {print}')
        case "$proxy_address" in
            *:*)
                route_family=6
                route_prefix=128
                route_line=$(ip -6 route get "$proxy_address" 2>/dev/null | awk 'NR==1 {print}')
                ;;
        esac
        route_dev=$(printf '%s\n' "$route_line" | awk '{for (i = 1; i <= NF; i++) if ($i == "dev") {print $(i + 1); exit}}')
        route_via=$(printf '%s\n' "$route_line" | awk '{for (i = 1; i <= NF; i++) if ($i == "via") {print $(i + 1); exit}}')
        if [ -z "$route_dev" ]; then
            echo "unable to determine uplink route for TUN proxy $proxy_address" >&2
            exit 1
        fi
        if [ "$route_family" -eq 6 ]; then
            if ip -6 route show exact "$proxy_address/$route_prefix" 2>/dev/null | grep -q .; then
                continue
            fi
        elif ip route show exact "$proxy_address/$route_prefix" 2>/dev/null | grep -q .; then
            continue
        fi
        if [ "$route_family" -eq 6 ]; then
            if [ -n "$route_via" ]; then
                ip -6 route add "$proxy_address/$route_prefix" via "$route_via" dev "$route_dev"
            else
                ip -6 route add "$proxy_address/$route_prefix" dev "$route_dev"
            fi
        elif [ -n "$route_via" ]; then
            ip route add "$proxy_address/$route_prefix" via "$route_via" dev "$route_dev"
        else
            ip route add "$proxy_address/$route_prefix" dev "$route_dev"
        fi
        printf '%s %s\n' "$route_family" "$proxy_address" >> "$proxy_route_file"
    done <<EOF
$proxy_addresses
EOF
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
    if [ -L "$resolv_conf" ]; then
        dns_symlink=1
        dns_link_target=$(readlink "$resolv_conf")
        if [ -e "$resolv_conf" ]; then cat "$resolv_conf" > "$dns_backup"; dns_was_present=1; fi
        rm -f "$resolv_conf"
    elif [ -e "$resolv_conf" ]; then
        cat "$resolv_conf" > "$dns_backup"
        dns_was_present=1
    fi
    printf 'nameserver %s\n' "$dns" > "$resolv_conf"
fi

ip route add 0.0.0.0/1 dev "$device" metric 1
route_added=1
ip route add 128.0.0.0/1 dev "$device" metric 1
if ip -6 route add ::/1 dev "$device" metric 1 2>/dev/null; then
    route6_first_added=1
    ip -6 route add 8000::/1 dev "$device" metric 1
    route6_second_added=1
fi

if [ "$uplink_fallback" -eq 1 ]; then
    echo "no default route; using loopback for local proxy"
fi
echo "transparent relay active: $device -> $proxy via $uplink"
"$tun2socks_bin" --device "$device" --proxy "$proxy" --interface "$uplink" &
tun_pid=$!
wait "$tun_pid"
