#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
broker_pid=
proxy_pid=
http_pid=
udp_pid=
curl_pid=
reverse_curl_pid=
udp_check_pid=

cleanup() {
    [ -z "$curl_pid" ] || kill "$curl_pid" 2>/dev/null || true
    [ -z "$reverse_curl_pid" ] || kill "$reverse_curl_pid" 2>/dev/null || true
    [ -z "$udp_check_pid" ] || kill "$udp_check_pid" 2>/dev/null || true
    [ -z "$proxy_pid" ] || kill "$proxy_pid" 2>/dev/null || true
    [ -z "$broker_pid" ] || kill "$broker_pid" 2>/dev/null || true
    [ -z "$http_pid" ] || kill "$http_pid" 2>/dev/null || true
    [ -z "$udp_pid" ] || kill "$udp_pid" 2>/dev/null || true
    [ -z "$curl_pid" ] || wait "$curl_pid" 2>/dev/null || true
    [ -z "$reverse_curl_pid" ] || wait "$reverse_curl_pid" 2>/dev/null || true
    [ -z "$udp_check_pid" ] || wait "$udp_check_pid" 2>/dev/null || true
    [ -z "$proxy_pid" ] || wait "$proxy_pid" 2>/dev/null || true
    [ -z "$broker_pid" ] || wait "$broker_pid" 2>/dev/null || true
    [ -z "$http_pid" ] || wait "$http_pid" 2>/dev/null || true
    [ -z "$udp_pid" ] || wait "$udp_pid" 2>/dev/null || true
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

python3 - >"$tmp_dir/udp.log" 2>&1 <<'PY' &
import socket

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.bind(("127.0.0.1", 18085))
while True:
    data, address = sock.recvfrom(65535)
    print("udp-started", flush=True)
    sock.sendto(data, address)
PY
udp_pid=$!

token=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
WSL_WIN_RELAY_ATTACH_TOKEN=$token \
WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/win-broker" -endpoint "$tmp_dir/broker.sock" -token-hex "$token" \
    >"$tmp_dir/broker.log" 2>&1 &
broker_pid=$!
for _ in $(seq 1 300); do
    [ -S "$tmp_dir/broker.sock" ] && break
    sleep 0.1
done
if [ ! -S "$tmp_dir/broker.sock" ]; then
    cat "$tmp_dir/broker.log"
    exit 1
fi

WSL_WIN_RELAY_ATTACH_TOKEN=$token \
WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/wsl-proxy" -broker-mode -relay-exe "$tmp_dir/win-connector" \
    -listen 127.0.0.1:18083 -control-socket "$tmp_dir/control.sock" \
    -reverse 127.0.0.1:18084=127.0.0.1:18082 \
    -reverse-udp 127.0.0.1:18086=127.0.0.1:18085 \
    >"$tmp_dir/proxy.log" 2>&1 &
proxy_pid=$!

curl --noproxy '' --silent --show-error --fail \
    --socks5-hostname 127.0.0.1:18083 \
    http://127.0.0.1:18082/ >"$tmp_dir/curl.out" 2>"$tmp_dir/curl.err" &
curl_pid=$!
for _ in $(seq 1 300); do
    grep -q request-started "$tmp_dir/http.log" && break
    sleep 0.1
done
if ! grep -q request-started "$tmp_dir/http.log"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/curl.err" "$tmp_dir/http.log"
    exit 1
fi

# Confirm the broker-owned Windows listener exists before replacing the
# connector. The probe sends no HTTP bytes, so it does not consume the
# delayed-response request used by the in-flight stream assertion.
for _ in $(seq 1 300); do
    if python3 - <<'PY'
import socket

sock = socket.socket()
sock.settimeout(0.2)
try:
    sock.connect(("127.0.0.1", 18084))
except OSError:
    raise SystemExit(1)
finally:
    sock.close()
PY
    then
        break
    fi
    sleep 0.1
done
if ! python3 - <<'PY'
import socket

sock = socket.socket()
sock.settimeout(1)
sock.connect(("127.0.0.1", 18084))
sock.close()
PY
then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log"
    exit 1
fi

connector_pid=
for _ in $(seq 1 100); do
    connector_pid=$(ps -o pid= --ppid "$proxy_pid" | awk 'NF {print $1; exit}')
    [ -n "$connector_pid" ] && break
    sleep 0.1
done
[ -n "$connector_pid" ]
kill "$connector_pid"

curl --noproxy '*' --silent --show-error --fail --max-time 20 \
    http://127.0.0.1:18084/ >"$tmp_dir/reverse-curl.out" 2>"$tmp_dir/reverse-curl.err" &
reverse_curl_pid=$!

python3 - >"$tmp_dir/udp-check.out" 2>"$tmp_dir/udp-check.err" <<'PY' &
import socket

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.settimeout(20)
sock.sendto(b"udp-reconnect", ("127.0.0.1", 18086))
data, _ = sock.recvfrom(65535)
if data != b"udp-reconnect":
    raise SystemExit("unexpected UDP response: %r" % (data,))
PY
udp_check_pid=$!

for _ in $(seq 1 300); do
    if ! kill -0 "$curl_pid" 2>/dev/null && ! kill -0 "$reverse_curl_pid" 2>/dev/null && ! kill -0 "$udp_check_pid" 2>/dev/null; then
        break
    fi
    sleep 0.1
done
if kill -0 "$curl_pid" 2>/dev/null; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/curl.err" "$tmp_dir/reverse-curl.err"
    exit 1
fi
if kill -0 "$reverse_curl_pid" 2>/dev/null; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/reverse-curl.err"
    exit 1
fi
if kill -0 "$udp_check_pid" 2>/dev/null; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/udp-check.err"
    exit 1
fi
wait "$curl_pid"
grep -qx "reconnect-ok" "$tmp_dir/curl.out"
if ! wait "$reverse_curl_pid"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/http.log" "$tmp_dir/reverse-curl.err"
    exit 1
fi
grep -qx "reconnect-ok" "$tmp_dir/reverse-curl.out"
wait "$udp_check_pid"
echo "broker connector preserved TCP, reverse-listener, and reverse-UDP flows across connector restart"
