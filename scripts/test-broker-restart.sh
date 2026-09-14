#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
broker_pid=
worker_pid=
host_pid=
proxy_pid=
http_pid=
http2_pid=
http3_pid=
delayed_pid=
delayed_curl_pid=
worker_delayed_curl_pid=
host_delayed_curl_pid=
owner_pid=
test_host=$(ip -o -4 addr show dev lo 2>/dev/null | awk '$4 !~ /^127\./ {split($4, fields, "/"); print fields[1]; exit}')
[ -n "$test_host" ] || test_host=127.0.0.1
export WWR_TEST_HOST=$test_host
service_port=$(python3 -c 'import socket
import os
while True:
    s=socket.socket(); s.bind((os.environ["WWR_TEST_HOST"], 0)); p=s.getsockname()[1]; s.close()
    if p <= 55000:
        print(p)
        break')
explicit_target_port=$((service_port + 1))
explicit_port=$((service_port + 2))
socks_port=$((service_port + 3))
strict_port=$((service_port + 4))
delayed_port=$((service_port + 5))
auto_windows_port=$((service_port + 10000))

cleanup() {
    [ -z "${proxy_pid:-}" ] || kill "$proxy_pid" 2>/dev/null || true
    [ -z "${broker_pid:-}" ] || kill "$broker_pid" 2>/dev/null || true
    [ -z "${worker_pid:-}" ] || kill "$worker_pid" 2>/dev/null || true
    [ -z "${host_pid:-}" ] || kill "$host_pid" 2>/dev/null || true
    [ -z "${owner_pid:-}" ] || kill "$owner_pid" 2>/dev/null || true
    [ -z "${http_pid:-}" ] || kill "$http_pid" 2>/dev/null || true
    [ -z "${http2_pid:-}" ] || kill "$http2_pid" 2>/dev/null || true
    [ -z "${http3_pid:-}" ] || kill "$http3_pid" 2>/dev/null || true
    [ -z "${delayed_pid:-}" ] || kill "$delayed_pid" 2>/dev/null || true
    [ -z "${delayed_curl_pid:-}" ] || kill "$delayed_curl_pid" 2>/dev/null || true
    [ -z "${worker_delayed_curl_pid:-}" ] || kill "$worker_delayed_curl_pid" 2>/dev/null || true
    [ -z "${host_delayed_curl_pid:-}" ] || kill "$host_delayed_curl_pid" 2>/dev/null || true
    [ -z "${proxy_pid:-}" ] || wait "$proxy_pid" 2>/dev/null || true
    [ -z "${broker_pid:-}" ] || wait "$broker_pid" 2>/dev/null || true
    [ -z "${worker_pid:-}" ] || wait "$worker_pid" 2>/dev/null || true
    [ -z "${host_pid:-}" ] || wait "$host_pid" 2>/dev/null || true
    [ -z "${owner_pid:-}" ] || wait "$owner_pid" 2>/dev/null || true
    [ -z "${http_pid:-}" ] || wait "$http_pid" 2>/dev/null || true
    [ -z "${http2_pid:-}" ] || wait "$http2_pid" 2>/dev/null || true
    [ -z "${http3_pid:-}" ] || wait "$http3_pid" 2>/dev/null || true
    [ -z "${delayed_pid:-}" ] || wait "$delayed_pid" 2>/dev/null || true
    [ -z "${delayed_curl_pid:-}" ] || wait "$delayed_curl_pid" 2>/dev/null || true
    [ -z "${worker_delayed_curl_pid:-}" ] || wait "$worker_delayed_curl_pid" 2>/dev/null || true
    [ -z "${host_delayed_curl_pid:-}" ] || wait "$host_delayed_curl_pid" 2>/dev/null || true
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

wait_for_socket() {
	path=$1
	for _ in $(seq 1 100); do
		[ -S "$path" ] && return 0
		sleep 0.1
	done
	cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" 2>/dev/null || true
	ss -ltnp || true
	return 1
}

GOPROXY=off go build -o "$tmp_dir/win-broker" "$repo_dir/cmd/win-broker"
GOPROXY=off go build -o "$tmp_dir/win-connector" "$repo_dir/cmd/win-connector"
GOPROXY=off go build -o "$tmp_dir/wsl-proxy" "$repo_dir/cmd/wsl-proxy"

printf '%s\n' 'broker-restart-ok' >"$tmp_dir/index.html"
(cd "$tmp_dir" && python3 -m http.server "$service_port" --bind "$test_host") >"$tmp_dir/http.log" 2>&1 &
http_pid=$!
sleep 0.2
if ! kill -0 "$http_pid" 2>/dev/null; then
    cat "$tmp_dir/http.log"
    exit 1
fi

(cd "$tmp_dir" && python3 -m http.server "$explicit_target_port" --bind "$test_host") >"$tmp_dir/http2.log" 2>&1 &
http2_pid=$!
(cd "$tmp_dir" && python3 -m http.server "$strict_port" --bind "$test_host") >"$tmp_dir/http3.log" 2>&1 &
http3_pid=$!
python3 - "$delayed_port" >"$tmp_dir/delayed.log" 2>&1 <<'PY' &
import http.server
import os
import sys
import time

port = int(sys.argv[1])

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/worker":
            print("worker-delayed-request", flush=True)
            body = b"broker-worker-crash-ok\n"
        elif self.path == "/host":
            print("host-delayed-request", flush=True)
            body = b"broker-socket-host-crash-ok\n"
        else:
            print("delayed-request", flush=True)
            body = b"broker-frontend-crash-ok\n"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        time.sleep(5)
        self.wfile.write(body)
        self.wfile.flush()

    def log_message(self, format, *args):
        pass

http.server.ThreadingHTTPServer((os.environ["WWR_TEST_HOST"], port), Handler).serve_forever()
PY
delayed_pid=$!

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
wait_for_socket "$tmp_dir/broker.sock"

WSL_WIN_RELAY_ATTACH_TOKEN=$token \
WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/wsl-proxy" -broker-mode -relay-exe "$tmp_dir/win-connector" \
    -listen "auto:$socks_port" -listen-status "$tmp_dir/listeners.json" -auto-forward \
    -auto-forward-host "$test_host" -auto-forward-port-offset 10000 -auto-forward-include "$service_port" \
    -auto-forward-interval 100ms -reverse "[::1]:$explicit_port=$test_host:$explicit_target_port" \
    -reverse "[::1]:$delayed_port=$test_host:$delayed_port" \
    -strict-listen-host6 ::1 \
    -control-socket "$tmp_dir/control.sock" >"$tmp_dir/proxy.log" 2>&1 &
proxy_pid=$!

probe() {
    curl --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://$test_host:$auto_windows_port/"
}

for _ in $(seq 1 300); do
    if probe >"$tmp_dir/first.out" 2>"$tmp_dir/first.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/first.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/http.log" "$tmp_dir/first.err"
    ss -ltnp || true
    exit 1
fi

for _ in $(seq 1 150); do
    if curl --globoff --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://[::1]:$explicit_port/" >"$tmp_dir/explicit-first.out" 2>"$tmp_dir/explicit-first.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/explicit-first.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/explicit-first.err"
    exit 1
fi

strict_lease=$(python3 - "$tmp_dir/control.sock" "$http3_pid" "$strict_port" <<'PY'
import os
import socket
import sys

path, pid, port = sys.argv[1], sys.argv[2], sys.argv[3]
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
    conn.connect(path)
    conn.sendall(f"RESERVE {pid} tcp6 {port} {os.environ['WWR_TEST_HOST']}\n".encode())
    response = conn.recv(256).decode()
if not response.startswith("OK "):
    raise SystemExit(response.strip())
lease = response.split()[1]
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
    conn.connect(path)
    conn.sendall(f"COMMIT {lease}\n".encode())
    response = conn.recv(256).decode()
if response.strip() != "OK":
    raise SystemExit(response.strip())
print(lease)
PY
)
for _ in $(seq 1 150); do
    if curl --globoff --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://[::1]:$strict_port/" >"$tmp_dir/strict-first.out" 2>"$tmp_dir/strict-first.err"; then
        break
    fi
    sleep 0.1
done
grep -qx "broker-restart-ok" "$tmp_dir/strict-first.out"

curl --globoff --noproxy '*' --silent --show-error --fail --max-time 20 \
    "http://[::1]:$delayed_port/" >"$tmp_dir/delayed.out" 2>"$tmp_dir/delayed.err" &
delayed_curl_pid=$!
for _ in $(seq 1 150); do
    grep -q delayed-request "$tmp_dir/delayed.log" && break
    sleep 0.1
done
if ! grep -q delayed-request "$tmp_dir/delayed.log"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/delayed.err" "$tmp_dir/delayed.log"
    exit 1
fi

kill -9 "$broker_pid"
wait "$broker_pid" 2>/dev/null || true
broker_pid=

WSL_WIN_RELAY_ATTACH_TOKEN=$token \
WSL_WIN_RELAY_BROKER_ENDPOINT="$tmp_dir/broker.sock" \
    "$tmp_dir/win-broker" -endpoint "$tmp_dir/broker.sock" -token-hex "$token" \
    >>"$tmp_dir/broker.log" 2>&1 &
broker_pid=$!
for _ in $(seq 1 100); do
	[ -S "$tmp_dir/broker.sock" ] && break
	sleep 0.1
done
wait_for_socket "$tmp_dir/broker.sock"

for _ in $(seq 1 450); do
    if probe >"$tmp_dir/second.out" 2>"$tmp_dir/second.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/second.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/second.err"
    exit 1
fi
for _ in $(seq 1 150); do
    if curl --globoff --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://[::1]:$explicit_port/" >"$tmp_dir/explicit-second.out" 2>"$tmp_dir/explicit-second.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/explicit-second.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/explicit-second.err"
    exit 1
fi
for _ in $(seq 1 150); do
    if curl --globoff --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://[::1]:$strict_port/" >"$tmp_dir/strict-second.out" 2>"$tmp_dir/strict-second.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/strict-second.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/strict-second.err"
    exit 1
fi
if ! wait "$delayed_curl_pid"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/delayed.err"
    exit 1
fi
if ! grep -qx "broker-frontend-crash-ok" "$tmp_dir/delayed.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/delayed.err" "$tmp_dir/delayed.out"
    od -An -tx1c "$tmp_dir/delayed.out" || true
    exit 1
fi

worker_pid=$(ps -eo pid=,args= | awk -v exe="$tmp_dir/win-broker" '$0 ~ exe " -worker" {print $1; exit}')
if [ -z "$worker_pid" ]; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log"
    exit 1
fi
kill -9 "$worker_pid"
wait "$worker_pid" 2>/dev/null || true
worker_pid=

for _ in $(seq 1 450); do
    if probe >"$tmp_dir/worker-restart.out" 2>"$tmp_dir/worker-restart.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/worker-restart.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/worker-restart.err"
    exit 1
fi
for _ in $(seq 1 150); do
    if curl --globoff --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://[::1]:$explicit_port/" >"$tmp_dir/explicit-worker.out" 2>"$tmp_dir/explicit-worker.err"; then
        break
    fi
    sleep 0.1
done
grep -qx "broker-restart-ok" "$tmp_dir/explicit-worker.out"
for _ in $(seq 1 150); do
    if curl --globoff --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://[::1]:$strict_port/" >"$tmp_dir/strict-worker.out" 2>"$tmp_dir/strict-worker.err"; then
        break
    fi
    sleep 0.1
done
grep -qx "broker-restart-ok" "$tmp_dir/strict-worker.out"

curl --globoff --noproxy '*' --silent --show-error --fail --max-time 20 \
    "http://[::1]:$delayed_port/worker" >"$tmp_dir/worker-delayed.out" 2>"$tmp_dir/worker-delayed.err" &
worker_delayed_curl_pid=$!
for _ in $(seq 1 150); do
    grep -q worker-delayed-request "$tmp_dir/delayed.log" && break
    sleep 0.1
done
if ! grep -q worker-delayed-request "$tmp_dir/delayed.log"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/worker-delayed.err" "$tmp_dir/delayed.log"
    exit 1
fi
worker_pid=$(ps -eo pid=,args= | awk -v exe="$tmp_dir/win-broker" '$0 ~ exe " -worker" {print $1; exit}')
if [ -z "$worker_pid" ]; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log"
    exit 1
fi
kill -9 "$worker_pid"
wait "$worker_pid" 2>/dev/null || true
worker_pid=
if ! wait "$worker_delayed_curl_pid"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/worker-delayed.err"
    exit 1
fi
grep -qx "broker-worker-crash-ok" "$tmp_dir/worker-delayed.out"
grep -q "reusing broker socket host" "$tmp_dir/broker.log"
grep -q "start broker worker" "$tmp_dir/broker.log" || grep -q "broker bridge worker listening" "$tmp_dir/broker.log"

host_pid=$(ps -eo pid=,args= | awk -v exe="$tmp_dir/win-broker" '$0 ~ exe " -socket-host" {print $1; exit}')
if [ -z "$host_pid" ]; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log"
    exit 1
fi
curl --globoff --noproxy '*' --silent --show-error --fail --max-time 20 \
    "http://[::1]:$delayed_port/host" >"$tmp_dir/host-delayed.out" 2>"$tmp_dir/host-delayed.err" &
host_delayed_curl_pid=$!
for _ in $(seq 1 150); do
    grep -q host-delayed-request "$tmp_dir/delayed.log" && break
    sleep 0.1
done
if ! grep -q host-delayed-request "$tmp_dir/delayed.log"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/host-delayed.err" "$tmp_dir/delayed.log"
    exit 1
fi
kill -9 "$host_pid"
wait "$host_pid" 2>/dev/null || true
host_pid=
if ! wait "$host_delayed_curl_pid"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/host-delayed.err"
    exit 1
fi
host_delayed_curl_pid=
grep -qx "broker-socket-host-crash-ok" "$tmp_dir/host-delayed.out"

owner_pid=$(ps -eo pid=,args= | awk -v exe="$tmp_dir/win-broker" '$0 ~ exe " -socket-owner" {print $1; exit}')
if [ -z "$owner_pid" ]; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log"
    exit 1
fi
kill -9 "$owner_pid"
wait "$owner_pid" 2>/dev/null || true
owner_pid=
for _ in $(seq 1 450); do
    if probe >"$tmp_dir/host-restart.out" 2>"$tmp_dir/host-restart.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/host-restart.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/host-restart.err"
    exit 1
fi
echo "frontend, bridge-worker, and socket-host bridge crashes preserved streams; socket-owner crash rebuilt mappings"
