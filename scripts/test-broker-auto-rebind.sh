#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
broker_pid=
proxy_pid=
http_pid=

cleanup() {
    [ -z "${proxy_pid:-}" ] || kill "$proxy_pid" 2>/dev/null || true
    [ -z "${broker_pid:-}" ] || kill "$broker_pid" 2>/dev/null || true
    [ -z "${http_pid:-}" ] || kill "$http_pid" 2>/dev/null || true
    [ -z "${proxy_pid:-}" ] || wait "$proxy_pid" 2>/dev/null || true
    [ -z "${broker_pid:-}" ] || wait "$broker_pid" 2>/dev/null || true
    [ -z "${http_pid:-}" ] || wait "$http_pid" 2>/dev/null || true
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

GOPROXY=off go build -o "$tmp_dir/win-broker" "$repo_dir/cmd/win-broker"
GOPROXY=off go build -o "$tmp_dir/win-connector" "$repo_dir/cmd/win-connector"
GOPROXY=off go build -o "$tmp_dir/wsl-proxy" "$repo_dir/cmd/wsl-proxy"

test_host=$(ip -o -4 addr show dev lo 2>/dev/null | awk '$4 !~ /^127\./ {split($4, fields, "/"); print fields[1]; exit}')
[ -n "$test_host" ] || test_host=127.0.0.1
export WWR_TEST_HOST=$test_host

python3 - >"$tmp_dir/http.log" 2>&1 <<'PY' &
import http.server
import os

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"auto-reconnect-ok\n"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass

http.server.ThreadingHTTPServer((os.environ["WWR_TEST_HOST"], 18087), Handler).serve_forever()
PY
http_pid=$!

token=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
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
    "$tmp_dir/wsl-proxy" -broker-mode -relay-exe "$tmp_dir/win-connector" \
    -listen auto:18088 -listen-status "$tmp_dir/listeners.json" -auto-forward \
    -auto-forward-host "$test_host" -auto-forward-port-offset 1000 -auto-forward-include 18087 \
    -auto-forward-interval 100ms -control-socket "$tmp_dir/control.sock" \
    >"$tmp_dir/proxy.log" 2>&1 &
proxy_pid=$!

probe() {
    curl --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://$test_host:19087/"
}

for _ in $(seq 1 150); do
    if probe >"$tmp_dir/first.out" 2>"$tmp_dir/first.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "auto-reconnect-ok" "$tmp_dir/first.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/first.err"
    exit 1
fi

connector_pid=$(ps -o pid= --ppid "$proxy_pid" | awk 'NF {print $1; exit}')
[ -n "$connector_pid" ]
kill "$connector_pid"

for _ in $(seq 1 300); do
    if probe >"$tmp_dir/second.out" 2>"$tmp_dir/second.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "auto-reconnect-ok" "$tmp_dir/second.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/second.err"
    exit 1
fi
echo "broker connector preserved automatic WSL listener mapping across connector restart"
