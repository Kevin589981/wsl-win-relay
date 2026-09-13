#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
fake_bin=$tmp_dir/bin
mkdir -p "$fake_bin"
log_file=$tmp_dir/ip.log
active_file=$tmp_dir/tun2socks.active
relay_log=$tmp_dir/relay.log
resolv_target=$tmp_dir/resolv.target
resolv_conf=$tmp_dir/resolv.conf
relay_pid=
cleanup() {
    trap - EXIT INT TERM HUP QUIT
    if [ -n "${relay_pid:-}" ]; then
        kill "$relay_pid" 2>/dev/null || true
        wait "$relay_pid" 2>/dev/null || true
    fi
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM HUP QUIT

printf '%s\n' \
    '#!/bin/sh' \
    'printf "0\\n"' \
    >"$fake_bin/id"
printf '%s\n' \
    '#!/bin/sh' \
    'printf "%s\\n" "$*" >>"$WWR_TEST_IP_LOG"' \
    'case "$1 $2 $3 $4" in' \
    '  "link show"*) [ "$3" = "$WWR_TUN_DEVICE" ] && [ -f "$WWR_TEST_TUN_FILE" ] && exit 0 || exit 1 ;;' \
    '  "link del"*) rm -f "$WWR_TEST_TUN_FILE"; exit 0 ;;' \
    '  "tuntap add"*) : >"$WWR_TEST_TUN_FILE"; exit 0 ;;' \
    '  "-6 route add ::/1"*) [ "${WWR_TEST_IPV6_MODE:-unsupported}" = partial ] && exit 0 || exit 1 ;;' \
    '  "-6 route add 8000::/1"*) [ "${WWR_TEST_IPV6_MODE:-unsupported}" = partial ] && exit 1 || exit 0 ;;' \
    '  *) exit 0 ;;' \
    'esac' \
    >"$fake_bin/ip"
printf '%s\n' \
    '#!/bin/sh' \
    'printf "%s\\n" "$$" >"$WWR_TEST_TUN2SOCKS_ACTIVE"' \
    'trap "" TERM INT HUP' \
    'while :; do sleep 10; done' \
    >"$fake_bin/tun2socks"
chmod +x "$fake_bin/id" "$fake_bin/ip" "$fake_bin/tun2socks"
printf 'nameserver 192.0.2.53\n' >"$resolv_target"
ln -s "$(basename "$resolv_target")" "$resolv_conf"

export PATH="$fake_bin:$PATH"
export WWR_TUN2SOCKS_BIN="$fake_bin/tun2socks"
export WWR_TUN_DEVICE=wsl-win-relay-test-tun
export WWR_TUN_PROXY=socks5://127.0.0.1:1080
export WWR_UPLINK_INTERFACE=lo
export WWR_DNS=203.0.113.53
export WWR_RESOLV_CONF=$resolv_conf
export WWR_TEST_IP_LOG=$log_file
export WWR_TEST_TUN_FILE=$tmp_dir/tun-created
export WWR_TEST_TUN2SOCKS_ACTIVE=$active_file

"$repo_dir/scripts/transparent-relay.sh" >"$relay_log" 2>&1 &
relay_pid=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -f "$active_file" ] && break
    sleep 0.1
done
[ -f "$active_file" ] || { cat "$relay_log" >&2; exit 1; }
tun_process=$(cat "$active_file")

started=$(date +%s)
kill -HUP "$relay_pid"
set +e
wait "$relay_pid"
relay_status=$?
set -e
relay_pid=
elapsed=$(( $(date +%s) - started ))
[ "$elapsed" -lt 5 ] || { cat "$relay_log" >&2; echo "transparent relay shutdown exceeded bound" >&2; exit 1; }
[ "$relay_status" -ne 0 ] || { cat "$relay_log" >&2; echo "HUP unexpectedly reported success" >&2; exit 1; }
if kill -0 "$tun_process" 2>/dev/null; then
    echo "tun2socks process survived cleanup" >&2
    exit 1
fi
[ ! -f "$WWR_TEST_TUN_FILE" ] || { echo "TUN device survived cleanup" >&2; exit 1; }
if [ "$(readlink "$WWR_RESOLV_CONF")" != "$(basename "$resolv_target")" ]; then
    echo "resolv.conf symlink target was not restored" >&2
    exit 1
fi
grep -qx 'nameserver 192.0.2.53' "$WWR_RESOLV_CONF"
grep -q 'route del 0.0.0.0/1' "$log_file"
grep -q 'route del 128.0.0.0/1' "$log_file"
grep -q 'link del' "$log_file"

# A failure after the first IPv6 split route is installed must still remove
# that route before returning the startup error.
: >"$log_file"
rm -f "$active_file"
export WWR_TEST_IPV6_MODE=partial
set +e
"$repo_dir/scripts/transparent-relay.sh" >"$tmp_dir/partial.log" 2>&1
partial_status=$?
set -e
[ "$partial_status" -ne 0 ] || { cat "$tmp_dir/partial.log" >&2; echo "partial IPv6 route failure unexpectedly succeeded" >&2; exit 1; }
grep -q 'route del ::/1' "$log_file"
[ ! -f "$WWR_TEST_TUN_FILE" ] || { echo "TUN device survived partial IPv6 rollback" >&2; exit 1; }
if [ "$(readlink "$WWR_RESOLV_CONF")" != "$(basename "$resolv_target")" ]; then
    echo "resolv.conf symlink was not restored after partial IPv6 rollback" >&2
    exit 1
fi
grep -qx 'nameserver 192.0.2.53' "$WWR_RESOLV_CONF"
echo "transparent relay bounded shutdown and partial IPv6 rollback passed"
