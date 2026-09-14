#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
supervisor_pid=
second_supervisor_pid=
frontend_pid=
proxy_pid=
http_pid=

cleanup() {
    [ -z "${proxy_pid:-}" ] || kill "$proxy_pid" 2>/dev/null || true
    [ -z "${second_supervisor_pid:-}" ] || kill "$second_supervisor_pid" 2>/dev/null || true
    [ -z "${supervisor_pid:-}" ] || kill "$supervisor_pid" 2>/dev/null || true
    [ -z "${frontend_pid:-}" ] || kill "$frontend_pid" 2>/dev/null || true
    [ -z "${http_pid:-}" ] || kill "$http_pid" 2>/dev/null || true
    # A killed frontend may have left roles that it did not start in this
    # process. Only terminate broker binaries carrying this unique endpoint.
    for pid in $(ps -eo pid=,args= | awk -v marker="$tmp_dir/broker.sock" '$0 ~ marker {print $1}'); do
        kill "$pid" 2>/dev/null || true
    done
    [ -z "${proxy_pid:-}" ] || wait "$proxy_pid" 2>/dev/null || true
    [ -z "${supervisor_pid:-}" ] || wait "$supervisor_pid" 2>/dev/null || true
    [ -z "${second_supervisor_pid:-}" ] || wait "$second_supervisor_pid" 2>/dev/null || true
    [ -z "${http_pid:-}" ] || wait "$http_pid" 2>/dev/null || true
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

GOPROXY=off go build -o "$tmp_dir/win-broker" "$repo_dir/cmd/win-broker"
GOPROXY=off go build -o "$tmp_dir/win-connector" "$repo_dir/cmd/win-connector"
GOPROXY=off go build -o "$tmp_dir/wsl-proxy" "$repo_dir/cmd/wsl-proxy"

test_host=$(ip -o -4 addr show dev lo 2>/dev/null | awk '$4 !~ /^127\./ {split($4, fields, "/"); print fields[1]; exit}')
[ -n "$test_host" ] || test_host=127.0.0.1
python3 -m http.server 18082 --bind "$test_host" >"$tmp_dir/http.log" 2>&1 &
http_pid=$!

token=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
printf '%s\n' "$token" >"$tmp_dir/token"
chmod 600 "$tmp_dir/token"
WSL_WIN_RELAY_ATTACH_TOKEN=$token \
WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/win-broker" -supervise -endpoint "$tmp_dir/broker.sock" -token-file "$tmp_dir/token" \
    >"$tmp_dir/broker.log" 2>&1 &
supervisor_pid=$!

for _ in $(seq 1 100); do
    [ -S "$tmp_dir/broker.sock" ] && [ -S "$tmp_dir/broker.sock.control" ] && break
    sleep 0.1
done
[ -S "$tmp_dir/broker.sock" ]
[ -S "$tmp_dir/broker.sock.control" ]

WSL_WIN_RELAY_ATTACH_TOKEN=$token WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/wsl-proxy" -broker-mode -relay-exe "$tmp_dir/win-connector" \
    -listen auto:18083 -listen-status "$tmp_dir/listeners.json" -control-socket "$tmp_dir/control.sock" \
    >"$tmp_dir/proxy.log" 2>&1 &
proxy_pid=$!

probe() {
    proxy_address=$(sed -n 's/.*"socks5"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp_dir/listeners.json" 2>/dev/null | head -n 1)
    [ -n "$proxy_address" ] || return 1
    curl --noproxy '' --silent --show-error --fail \
        --socks5-hostname "$proxy_address" \
        "http://$test_host:18082/" >/dev/null 2>/dev/null
}

for _ in $(seq 1 100); do
    if probe; then
        break
    fi
    sleep 0.1
done
probe

# A second supervisor using the same endpoint must become a standby rather
# than creating a competing frontend. This models a WSL restart while the
# previous Windows process tree is still alive.
WSL_WIN_RELAY_ATTACH_TOKEN=$token WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/win-broker" -supervise -endpoint "$tmp_dir/broker.sock" -token-file "$tmp_dir/token" \
    >"$tmp_dir/second-broker.log" 2>&1 &
second_supervisor_pid=$!
for _ in $(seq 1 50); do
    if grep -q 'broker supervisor lock is owned by another process' "$tmp_dir/second-broker.log" 2>/dev/null; then
        break
    fi
    sleep 0.1
done
grep -q 'broker supervisor lock is owned by another process' "$tmp_dir/second-broker.log"
frontend_count=$(ps -eo args= | awk -v exe="$tmp_dir/win-broker" -v endpoint="$tmp_dir/broker.sock" \
    '$0 ~ exe && $0 ~ "-endpoint " endpoint && $0 !~ /-supervise/ && $0 !~ /-worker/ && $0 !~ /-socket-/ {count++} END {print count+0}')
[ "$frontend_count" -eq 1 ] || {
    cat "$tmp_dir/broker.log" "$tmp_dir/second-broker.log" >&2
    echo "competing broker frontend count: $frontend_count" >&2
    exit 1
}
kill "$second_supervisor_pid" 2>/dev/null || true
wait "$second_supervisor_pid" 2>/dev/null || true
second_supervisor_pid=

# Model WSL termination: the interop parent/supervisor disappears while its
# detached Windows frontend and socket-owning roles remain alive. The next WSL
# boot must authenticate and reuse that tree instead of racing its endpoint.
kill -9 "$supervisor_pid"
wait "$supervisor_pid" 2>/dev/null || true
supervisor_pid=
WSL_WIN_RELAY_ATTACH_TOKEN=$token WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/win-broker" -supervise -endpoint "$tmp_dir/broker.sock" -token-file "$tmp_dir/token" \
    >"$tmp_dir/replacement-supervisor.log" 2>&1 &
supervisor_pid=$!
for _ in $(seq 1 50); do
    if grep -q 'reusing authenticated broker frontend' "$tmp_dir/replacement-supervisor.log" 2>/dev/null; then
        break
    fi
    sleep 0.1
done
grep -q 'reusing authenticated broker frontend' "$tmp_dir/replacement-supervisor.log"
probe

for _ in $(seq 1 100); do
    frontend_pid=$(ps -eo pid=,args= | awk -v exe="$tmp_dir/win-broker" -v endpoint="$tmp_dir/broker.sock" '$0 ~ exe && $0 ~ endpoint && $0 !~ /-supervise/ {print $1; exit}')
    [ -n "$frontend_pid" ] && break
    sleep 0.1
done
[ -n "$frontend_pid" ]
kill -9 "$frontend_pid"
frontend_pid=

for _ in $(seq 1 400); do
    if probe; then
        echo "broker host supervisor rebuilt frontend and preserved WSL proxy service"
        exit 0
    fi
    sleep 0.1
done

cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log"
exit 1
