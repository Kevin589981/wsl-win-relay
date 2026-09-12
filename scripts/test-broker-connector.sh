#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
broker_pid=
proxy_pid=
http_pid=

cleanup() {
    [ -z "$proxy_pid" ] || kill "$proxy_pid" 2>/dev/null || true
    [ -z "$broker_pid" ] || kill "$broker_pid" 2>/dev/null || true
    [ -z "$http_pid" ] || kill "$http_pid" 2>/dev/null || true
    [ -z "$proxy_pid" ] || wait "$proxy_pid" 2>/dev/null || true
    [ -z "$broker_pid" ] || wait "$broker_pid" 2>/dev/null || true
    [ -z "$http_pid" ] || wait "$http_pid" 2>/dev/null || true
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

GOPROXY=off go build -o "$tmp_dir/win-broker" "$repo_dir/cmd/win-broker"
GOPROXY=off go build -o "$tmp_dir/win-connector" "$repo_dir/cmd/win-connector"
GOPROXY=off go build -o "$tmp_dir/wsl-proxy" "$repo_dir/cmd/wsl-proxy"

python3 -m http.server 18081 --bind 127.0.0.1 >"$tmp_dir/http.log" 2>&1 &
http_pid=$!

token=00112233445566778899aabbccddeeff
WSL_WIN_RELAY_ATTACH_TOKEN=$token \
WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/win-broker" -endpoint "$tmp_dir/broker.sock" -token-hex "$token" \
    >"$tmp_dir/broker.log" 2>&1 &
broker_pid=$!
for _ in $(seq 1 100); do
    [ -S "$tmp_dir/broker.sock" ] && break
    sleep 0.1
done
[ -S "$tmp_dir/broker.sock" ]

WSL_WIN_RELAY_ATTACH_TOKEN=$token \
WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/wsl-proxy" -relay-exe "$tmp_dir/win-connector" \
    -listen 127.0.0.1:18080 -control-socket "$tmp_dir/control.sock" \
    >"$tmp_dir/proxy.log" 2>&1 &
proxy_pid=$!

for _ in $(seq 1 100); do
    if curl --noproxy '' --silent --show-error --fail \
        --socks5-hostname 127.0.0.1:18080 \
        http://127.0.0.1:18081/ >/dev/null 2>/dev/null; then
        echo "broker-connector SOCKS5 integration passed"
        exit 0
    fi
    sleep 0.1
done

cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log"
exit 1
