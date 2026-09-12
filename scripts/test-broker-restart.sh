#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
broker_pid=
proxy_pid=
http_pid=
http2_pid=
http3_pid=
service_port=$(python3 -c 'import socket
while True:
    s=socket.socket(); s.bind(("127.0.0.1", 0)); p=s.getsockname()[1]; s.close()
    if p <= 65000:
        print(p)
        break')
explicit_target_port=$((service_port + 1))
explicit_port=$((service_port + 2))
socks_port=$((service_port + 3))
strict_port=$((service_port + 4))

cleanup() {
    [ -z "${proxy_pid:-}" ] || kill "$proxy_pid" 2>/dev/null || true
    [ -z "${broker_pid:-}" ] || kill "$broker_pid" 2>/dev/null || true
    [ -z "${http_pid:-}" ] || kill "$http_pid" 2>/dev/null || true
    [ -z "${http2_pid:-}" ] || kill "$http2_pid" 2>/dev/null || true
    [ -z "${http3_pid:-}" ] || kill "$http3_pid" 2>/dev/null || true
    [ -z "${proxy_pid:-}" ] || wait "$proxy_pid" 2>/dev/null || true
    [ -z "${broker_pid:-}" ] || wait "$broker_pid" 2>/dev/null || true
    [ -z "${http_pid:-}" ] || wait "$http_pid" 2>/dev/null || true
    [ -z "${http2_pid:-}" ] || wait "$http2_pid" 2>/dev/null || true
    [ -z "${http3_pid:-}" ] || wait "$http3_pid" 2>/dev/null || true
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

GOPROXY=off go build -o "$tmp_dir/win-broker" "$repo_dir/cmd/win-broker"
GOPROXY=off go build -o "$tmp_dir/win-connector" "$repo_dir/cmd/win-connector"
GOPROXY=off go build -o "$tmp_dir/wsl-proxy" "$repo_dir/cmd/wsl-proxy"

printf '%s\n' 'broker-restart-ok' >"$tmp_dir/index.html"
(cd "$tmp_dir" && python3 -m http.server "$service_port" --bind 127.0.0.1) >"$tmp_dir/http.log" 2>&1 &
http_pid=$!
sleep 0.2
if ! kill -0 "$http_pid" 2>/dev/null; then
    cat "$tmp_dir/http.log"
    exit 1
fi

(cd "$tmp_dir" && python3 -m http.server "$explicit_target_port" --bind 127.0.0.1) >"$tmp_dir/http2.log" 2>&1 &
http2_pid=$!
(cd "$tmp_dir" && python3 -m http.server "$strict_port" --bind 127.0.0.1) >"$tmp_dir/http3.log" 2>&1 &
http3_pid=$!

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
    -listen "127.0.0.1:$socks_port" -auto-forward \
    -auto-forward-host 127.0.0.2 -auto-forward-include "$service_port" \
    -auto-forward-interval 100ms -reverse "127.0.0.2:$explicit_port=127.0.0.1:$explicit_target_port" \
    -strict-listen-host 127.0.0.2 \
    -control-socket "$tmp_dir/control.sock" >"$tmp_dir/proxy.log" 2>&1 &
proxy_pid=$!

probe() {
    curl --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://127.0.0.2:$service_port/"
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
    if curl --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://127.0.0.2:$explicit_port/" >"$tmp_dir/explicit-first.out" 2>"$tmp_dir/explicit-first.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/explicit-first.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/explicit-first.err"
    exit 1
fi

strict_lease=$(python3 - "$tmp_dir/control.sock" "$http3_pid" "$strict_port" <<'PY'
import socket
import sys

path, pid, port = sys.argv[1], sys.argv[2], sys.argv[3]
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
    conn.connect(path)
    conn.sendall(f"RESERVE {pid} tcp4 {port} 127.0.0.1\n".encode())
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
    if curl --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://127.0.0.2:$strict_port/" >"$tmp_dir/strict-first.out" 2>"$tmp_dir/strict-first.err"; then
        break
    fi
    sleep 0.1
done
grep -qx "broker-restart-ok" "$tmp_dir/strict-first.out"

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
[ -S "$tmp_dir/broker.sock" ]

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
    if curl --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://127.0.0.2:$explicit_port/" >"$tmp_dir/explicit-second.out" 2>"$tmp_dir/explicit-second.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/explicit-second.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/explicit-second.err"
    exit 1
fi
for _ in $(seq 1 150); do
    if curl --noproxy '*' --silent --show-error --fail --max-time 2 \
        "http://127.0.0.2:$strict_port/" >"$tmp_dir/strict-second.out" 2>"$tmp_dir/strict-second.err"; then
        break
    fi
    sleep 0.1
done
if ! grep -qx "broker-restart-ok" "$tmp_dir/strict-second.out"; then
    cat "$tmp_dir/broker.log" "$tmp_dir/proxy.log" "$tmp_dir/strict-second.err"
    exit 1
fi
grep -q "broker instance changed" "$tmp_dir/proxy.log"
echo "broker restart rebuilt automatic, explicit, and strict mappings"
