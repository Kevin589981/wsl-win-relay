#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
broker_pid=
proxy_pid=
http_pid=
curl_pid=

cleanup() {
    [ -z "$curl_pid" ] || kill "$curl_pid" 2>/dev/null || true
    [ -z "$proxy_pid" ] || kill "$proxy_pid" 2>/dev/null || true
    [ -z "$broker_pid" ] || kill "$broker_pid" 2>/dev/null || true
    [ -z "$http_pid" ] || kill "$http_pid" 2>/dev/null || true
    [ -z "$curl_pid" ] || wait "$curl_pid" 2>/dev/null || true
    [ -z "$proxy_pid" ] || wait "$proxy_pid" 2>/dev/null || true
    [ -z "$broker_pid" ] || wait "$broker_pid" 2>/dev/null || true
    [ -z "$http_pid" ] || wait "$http_pid" 2>/dev/null || true
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

GOPROXY=off go build -o "$tmp_dir/win-broker" "$repo_dir/cmd/win-broker"
GOPROXY=off go build -o "$tmp_dir/win-connector" "$repo_dir/cmd/win-connector"
GOPROXY=off go build -o "$tmp_dir/wsl-proxy" "$repo_dir/cmd/wsl-proxy"

python3 - >"$tmp_dir/http.log" 2>&1 <<'PY' &
import http.server
import time

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        print("request-started", flush=True)
        body = b"reconnect-ok\n"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        time.sleep(4)
        self.wfile.write(body)
        self.wfile.flush()

    def log_message(self, format, *args):
        pass

http.server.ThreadingHTTPServer(("127.0.0.1", 18082), Handler).serve_forever()
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
    -listen 127.0.0.1:18083 -control-socket "$tmp_dir/control.sock" \
    >"$tmp_dir/proxy.log" 2>&1 &
proxy_pid=$!

curl --noproxy '' --silent --show-error --fail \
    --socks5-hostname 127.0.0.1:18083 \
    http://127.0.0.1:18082/ >"$tmp_dir/curl.out" 2>"$tmp_dir/curl.err" &
curl_pid=$!
for _ in $(seq 1 100); do
    grep -q request-started "$tmp_dir/http.log" && break
    sleep 0.1
done
grep -q request-started "$tmp_dir/http.log"

connector_pid=$(ps -o pid= --ppid "$proxy_pid" | awk 'NF {print $1; exit}')
[ -n "$connector_pid" ]
kill "$connector_pid"

for _ in $(seq 1 300); do
    kill -0 "$curl_pid" 2>/dev/null || break
    sleep 0.1
done
if kill -0 "$curl_pid" 2>/dev/null; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/curl.err"
    exit 1
fi
wait "$curl_pid"
grep -qx "reconnect-ok" "$tmp_dir/curl.out"
echo "broker connector preserved in-flight SOCKS5 stream across connector restart"
