#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
proxy_bin=${WWR_PROXY_BIN:-"$repo_dir/bin/wsl-proxy-linux"}
broker_exe=${WWR_BROKER_EXE:-"$repo_dir/bin/wsl-win-broker.exe"}
connector_exe=${WWR_CONNECTOR_EXE:-"$repo_dir/bin/wsl-win-connector.exe"}
socks_listen=${WWR_BROKER_INTEROP_LISTEN:-}
http_listen=${WWR_BROKER_INTEROP_HTTP_LISTEN:-}
reverse_port=${WWR_BROKER_INTEROP_REVERSE_PORT:-}
auto_port=${WWR_BROKER_INTEROP_AUTO_PORT:-}
auto_forward_offset=${WWR_BROKER_INTEROP_AUTO_FORWARD_OFFSET:-0}
windows_port_auto=${WWR_BROKER_INTEROP_WINDOWS_PORT_AUTO:-0}
auto_udp=${WWR_BROKER_INTEROP_AUTO_UDP:-0}
reverse_wsl_host=${WWR_BROKER_INTEROP_WSL_HOST:-127.0.0.2}
endpoint=${WWR_BROKER_INTEROP_ENDPOINT:-"wsl-win-relay-interop-$$"}
upstream_proxy=${WWR_WINDOWS_UPSTREAM_PROXY:-}
service_wrapper=${WWR_BROKER_INTEROP_SERVICE_WRAPPER:-0}
windows_shell=${WWR_WINDOWS_SHELL:-powershell.exe}
case "$auto_forward_offset" in
	-[0-9]*|[0-9]*) ;;
	*) echo "WWR_BROKER_INTEROP_AUTO_FORWARD_OFFSET must be an integer" >&2; exit 1 ;;
esac
case "$windows_port_auto" in
	0|1) ;;
	*) echo "WWR_BROKER_INTEROP_WINDOWS_PORT_AUTO must be 0 or 1" >&2; exit 1 ;;
esac
case "$auto_udp" in
	0|1) ;;
	*) echo "WWR_BROKER_INTEROP_AUTO_UDP must be 0 or 1" >&2; exit 1 ;;
esac
if [ "$windows_port_auto" -eq 1 ] && [ "$auto_forward_offset" -ne 0 ]; then
	echo "Windows automatic port allocation cannot be combined with an offset" >&2
	exit 1
fi
if [ "$auto_udp" -eq 1 ] && [ "$windows_port_auto" -ne 1 ]; then
	echo "the automatic UDP interop check requires Windows automatic port allocation" >&2
	exit 1
fi
work=$(mktemp -d "${TMPDIR:-/tmp}/wsl-win-relay-broker-interop.XXXXXX")
broker_pid=
proxy_pid=
http_pid=
auto_http_pid=
connector_pid=
auto_udp_pid=

cleanup() {
	stop_windows_roles || true
	[ -z "${proxy_pid:-}" ] || kill "$proxy_pid" 2>/dev/null || true
	[ -z "${connector_pid:-}" ] || kill "$connector_pid" 2>/dev/null || true
	[ -z "${http_pid:-}" ] || kill "$http_pid" 2>/dev/null || true
	[ -z "${auto_http_pid:-}" ] || kill "$auto_http_pid" 2>/dev/null || true
	[ -z "${auto_udp_pid:-}" ] || kill "$auto_udp_pid" 2>/dev/null || true
	[ -z "${broker_pid:-}" ] || kill "$broker_pid" 2>/dev/null || true
	[ -z "${proxy_pid:-}" ] || wait "$proxy_pid" 2>/dev/null || true
	[ -z "${http_pid:-}" ] || wait "$http_pid" 2>/dev/null || true
	[ -z "${auto_http_pid:-}" ] || wait "$auto_http_pid" 2>/dev/null || true
	[ -z "${auto_udp_pid:-}" ] || wait "$auto_udp_pid" 2>/dev/null || true
	[ -z "${broker_pid:-}" ] || wait "$broker_pid" 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

stop_windows_roles() {
	if [ ! -x "$windows_shell" ] && ! command -v "$windows_shell" >/dev/null 2>&1; then
		return 0
	fi
	# WSL's kill only reaches the interop shim, not the Windows descendants.
	# Match the unique endpoint so cleanup cannot terminate another broker.
	"$windows_shell" -NoProfile -NonInteractive -Command \
		"\$needle='-endpoint $endpoint'; Get-CimInstance Win32_Process | Where-Object { \$_.Name -eq 'wsl-win-broker.exe' -and \$_.CommandLine -like ('*' + \$needle + '*') } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force -ErrorAction SilentlyContinue }" \
		>/dev/null 2>&1 || true
}

if [ ! -x "$proxy_bin" ]; then
	echo "missing Linux proxy: $proxy_bin (run scripts/build-wsl.sh)" >&2
	exit 1
fi
if [ ! -f "$broker_exe" ] || [ ! -f "$connector_exe" ]; then
	echo "missing Windows broker/connector (run scripts/build-wsl.sh)" >&2
	exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
	echo "curl is required for the Windows broker interop smoke" >&2
	exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
	echo "python3 is required for the reverse forwarding interop smoke" >&2
	exit 1
fi
if [ ! -x "$windows_shell" ] && ! command -v "$windows_shell" >/dev/null 2>&1; then
	echo "Windows PowerShell is required for broker cleanup: $windows_shell (set WWR_WINDOWS_SHELL)" >&2
	exit 1
fi
pick_free_port() {
	python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
pick_free_udp_port() {
	python3 -c 'import socket, sys; s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind((sys.argv[1], 0)); print(s.getsockname()[1]); s.close()' "$reverse_wsl_host"
}
status_windows_port() {
	python3 -c 'import json, sys; document=json.load(open(sys.argv[1], encoding="utf-8")); print(next((entry["windows_address"].rsplit(":", 1)[1] for entry in document["mappings"] if entry["network"] == sys.argv[2] and entry["wsl_address"] == sys.argv[3]), ""))' "$1" "$2" "$3" 2>/dev/null
}
pick_windows_free_port() {
	"$windows_shell" -NoProfile -NonInteractive -Command \
		"\$listener = [System.Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0); \$listener.Start(); [Console]::Out.Write((\$listener.LocalEndpoint).Port); \$listener.Stop()" | tr -d '\r'
}
port_available_in_wsl() {
	python3 -c 'import socket, sys; s = socket.socket(); s.bind((sys.argv[1], int(sys.argv[2]))); s.close()' "$reverse_wsl_host" "$1" >/dev/null 2>&1
}
pick_shared_free_port() {
	for _ in $(seq 1 20); do
		candidate=$(pick_windows_free_port) || continue
		case "$candidate" in
			''|*[!0-9]*) continue ;;
		esac
		if port_available_in_wsl "$candidate"; then
			printf '%s\n' "$candidate"
			return 0
		fi
	done
	echo "could not find a TCP port available in both Windows and WSL" >&2
	return 1
}
port_available_in_windows() {
	"$windows_shell" -NoProfile -NonInteractive -Command \
		"try { \$listener = [System.Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $1); \$listener.Start(); \$listener.Stop(); exit 0 } catch { exit 1 }" \
		>/dev/null 2>&1
}
pick_auto_port() {
	if [ "$windows_port_auto" -eq 1 ]; then
		pick_free_port
		return
	fi
	if [ "$auto_forward_offset" -eq 0 ]; then
		pick_shared_free_port
		return
	fi
	for _ in $(seq 1 40); do
		candidate=$(pick_free_port) || continue
		target=$((candidate + auto_forward_offset))
		if [ "$target" -lt 1 ] || [ "$target" -gt 65535 ]; then
			continue
		fi
		if port_available_in_wsl "$candidate" && port_available_in_windows "$target"; then
			printf '%s\n' "$candidate"
			return 0
		fi
	done
	echo "could not find ports for automatic mapping offset $auto_forward_offset" >&2
	return 1
}
if [ -z "$socks_listen" ]; then
	socks_listen="127.0.0.1:$(pick_free_port)"
fi
if [ -z "$http_listen" ]; then
	http_listen="127.0.0.1:$(pick_free_port)"
fi
if [ -z "$reverse_port" ]; then
	reverse_port=$(pick_shared_free_port)
fi
if [ -z "$auto_port" ]; then
	auto_port=$(pick_auto_port)
fi
auto_windows_port=
auto_udp_port=
auto_udp_windows_port=
if [ "$auto_udp" -eq 1 ]; then
	auto_udp_port=$(pick_free_udp_port)
fi
if [ "$windows_port_auto" -eq 0 ]; then
	auto_windows_port=$((auto_port + auto_forward_offset))
	if [ "$auto_windows_port" -lt 1 ] || [ "$auto_windows_port" -gt 65535 ]; then
		echo "automatic Windows port $auto_windows_port is outside 1..65535" >&2
		exit 1
	fi
fi

token=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
start_broker() {
	if [ "$service_wrapper" = 1 ]; then
		broker_env="$work/broker.env"
		{
			printf '%s\n' "WSL_WIN_RELAY_BROKER_EXE=$broker_exe"
			printf '%s\n' "WSL_WIN_RELAY_BROKER_ENDPOINT=$endpoint"
			printf '%s\n' "WSL_WIN_RELAY_ATTACH_TOKEN=$token"
			if [ -n "$upstream_proxy" ]; then
				printf '%s\n' "WSL_WIN_RELAY_UPSTREAM_PROXY=$upstream_proxy"
			fi
		} >"$broker_env"
		chmod 600 "$broker_env"
		WSL_WIN_RELAY_BROKER_ENV_FILE="$broker_env" \
			"$repo_dir/scripts/run-broker-user-service.sh"
		return
	fi
	if [ -n "$upstream_proxy" ]; then
		"$broker_exe" -endpoint "$endpoint" -token-hex "$token" -upstream-proxy "$upstream_proxy"
		return
	fi
	"$broker_exe" -endpoint "$endpoint" -token-hex "$token"
}
start_broker >"$work/broker.log" 2>&1 &
broker_pid=$!
sleep 1

printf '%s\n' "wsl-win-relay reverse interop $endpoint" >"$work/index.html"
python3 -m http.server "$reverse_port" --bind "$reverse_wsl_host" --directory "$work" >"$work/http.log" 2>&1 &
http_pid=$!
mkdir "$work/auto"
printf '%s\n' "wsl-win-relay automatic interop $endpoint" >"$work/auto/index.html"
python3 -m http.server "$auto_port" --bind "$reverse_wsl_host" --directory "$work/auto" >"$work/auto-http.log" 2>&1 &
auto_http_pid=$!
if [ "$auto_udp" -eq 1 ]; then
	python3 -u -c 'import socket, sys; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.bind((sys.argv[1], int(sys.argv[2]))); exec("while True:\n data, peer = s.recvfrom(65535)\n s.sendto(data, peer)")' "$reverse_wsl_host" "$auto_udp_port" >"$work/auto-udp.log" 2>&1 &
	auto_udp_pid=$!
fi
sleep 0.5

wslenv=${WSLENV:-}
for required in WSL_WIN_RELAY_BROKER_ENDPOINT WSL_WIN_RELAY_ATTACH_TOKEN; do
	case ":$wslenv:" in
	*":$required:"*) ;;
	*) wslenv=${wslenv:+"$wslenv:"}"$required" ;;
	esac
done
WSLENV="$wslenv" \
WSL_WIN_RELAY_BROKER_ENDPOINT="$endpoint" \
WSL_WIN_RELAY_ATTACH_TOKEN="$token" \
	sh -c 'auto=$1; offset=$2; udp=$3; udp_port=$4; shift 4; if [ "$auto" -eq 1 ]; then set -- "$@" -auto-forward-port-auto; else set -- "$@" -auto-forward-port-offset "$offset"; fi; if [ "$udp" -eq 1 ]; then set -- "$@" -auto-forward-udp -auto-forward-udp-include "$udp_port"; fi; exec "$@"' sh "$windows_port_auto" "$auto_forward_offset" "$auto_udp" "$auto_udp_port" \
	"$proxy_bin" -broker-mode -relay-exe "$connector_exe" -listen "$socks_listen" \
		-http-listen "$http_listen" \
		-reverse "127.0.0.1:$reverse_port=$reverse_wsl_host:$reverse_port" \
		-auto-forward -auto-forward-status "$work/auto-mappings.json" -auto-forward-include "$auto_port" >"$work/proxy.log" 2>&1 &
proxy_pid=$!

for _ in $(seq 1 60); do
	if grep -q "Windows broker connector ready" "$work/proxy.log"; then
		break
	fi
	sleep 0.5
done
if ! grep -q "Windows broker connector ready" "$work/proxy.log"; then
	echo "Windows broker connector did not attach" >&2
	cat "$work/broker.log" "$work/proxy.log" >&2 || true
	exit 1
fi

if ! curl --noproxy "" --proxy "socks5h://$socks_listen" --connect-timeout 5 --max-time 15 -fsS https://example.com/ >"$work/response.html" 2>"$work/curl.err"; then
	echo "Windows broker SOCKS5 request failed" >&2
	cat "$work/broker.log" "$work/proxy.log" "$work/curl.err" >&2 || true
	exit 1
fi
grep -qi "Example Domain" "$work/response.html"
if ! curl --noproxy "" --proxy "http://$http_listen" --connect-timeout 5 --max-time 15 -fsS http://example.com/ >"$work/http-response.html" 2>"$work/http-curl.err"; then
	echo "Windows broker HTTP proxy request failed" >&2
	cat "$work/broker.log" "$work/proxy.log" "$work/http-curl.err" >&2 || true
	exit 1
fi
grep -qi "Example Domain" "$work/http-response.html"

connector_pid=$(pgrep -P "$proxy_pid" -f 'wsl-win-connector\.exe' | head -n 1 || true)
if [ -z "$connector_pid" ]; then
	echo "could not locate the Windows broker connector child" >&2
	cat "$work/broker.log" "$work/proxy.log" >&2 || true
	exit 1
fi
ready_before=$(grep -c "Windows broker connector ready" "$work/proxy.log" || true)
kill -9 "$connector_pid" 2>/dev/null || true
connector_pid=
for _ in $(seq 1 60); do
	ready_after=$(grep -c "Windows broker connector ready" "$work/proxy.log" || true)
	if [ "$ready_after" -gt "$ready_before" ]; then
		break
	fi
	sleep 0.5
done
if [ "${ready_after:-0}" -le "$ready_before" ]; then
	echo "Windows broker connector did not reattach after restart" >&2
	cat "$work/broker.log" "$work/proxy.log" >&2 || true
	exit 1
fi

if [ "$windows_port_auto" -eq 1 ]; then
	for _ in $(seq 1 60); do
		auto_windows_port=$(status_windows_port "$work/auto-mappings.json" tcp4 "$reverse_wsl_host:$auto_port" || true)
		[ -z "$auto_windows_port" ] || break
		sleep 0.5
	done
	if [ -z "$auto_windows_port" ]; then
		echo "Windows did not report an automatically allocated port" >&2
		cat "$work/auto-mappings.json" "$work/proxy.log" >&2 || true
		exit 1
	fi
fi

if [ "$auto_udp" -eq 1 ]; then
	for _ in $(seq 1 60); do
		auto_udp_windows_port=$(status_windows_port "$work/auto-mappings.json" udp4 "$reverse_wsl_host:$auto_udp_port" || true)
		[ -z "$auto_udp_windows_port" ] || break
		sleep 0.5
	done
	if [ -z "$auto_udp_windows_port" ]; then
		echo "Windows did not report an automatically allocated UDP port" >&2
		cat "$work/auto-mappings.json" "$work/proxy.log" "$work/auto-udp.log" >&2 || true
		exit 1
	fi
	if ! "$windows_shell" -NoProfile -NonInteractive -Command \
		"\$client = [Net.Sockets.UdpClient]::new(); \$client.Client.ReceiveTimeout = 10000; \$payload = [Text.Encoding]::UTF8.GetBytes('udp-$endpoint'); [void]\$client.Send(\$payload, \$payload.Length, '127.0.0.1', $auto_udp_windows_port); \$remote = [Net.IPEndPoint]::new([Net.IPAddress]::Any, 0); \$response = \$client.Receive([ref]\$remote); \$client.Dispose(); [Console]::Out.Write([Text.Encoding]::UTF8.GetString(\$response))" \
		>"$work/auto-udp-response.txt" 2>"$work/auto-udp-reverse.err"; then
		echo "Windows automatic UDP mapping request failed" >&2
		cat "$work/proxy.log" "$work/auto-udp.log" "$work/auto-udp-reverse.err" >&2 || true
		exit 1
	fi
	grep -qx "udp-$endpoint" "$work/auto-udp-response.txt"
	kill "$auto_udp_pid" 2>/dev/null || true
	wait "$auto_udp_pid" 2>/dev/null || true
	auto_udp_pid=
	for _ in $(seq 1 60); do
		if grep -q "auto-forward UDP removed Windows port $auto_udp_windows_port (udp4)" "$work/proxy.log"; then
			break
		fi
		sleep 0.5
	done
	if ! grep -q "auto-forward UDP removed Windows port $auto_udp_windows_port (udp4)" "$work/proxy.log"; then
		echo "Windows automatic UDP mapping was not removed" >&2
		cat "$work/proxy.log" "$work/auto-udp.log" >&2 || true
		exit 1
	fi
fi

for _ in $(seq 1 60); do
	if grep -q "auto-forward added 127.0.0.1:$auto_windows_port" "$work/proxy.log" && \
		grep -q '"windows_address": "127.0.0.1:'"$auto_windows_port"'"' "$work/auto-mappings.json" && \
		grep -q '"wsl_address": "127.0.0.2:'"$auto_port"'"' "$work/auto-mappings.json"; then
		break
	fi
	sleep 0.5
done
if ! grep -q "auto-forward added 127.0.0.1:$auto_windows_port" "$work/proxy.log"; then
	echo "Windows automatic mapping was not created" >&2
	cat "$work/broker.log" "$work/proxy.log" "$work/auto-http.log" >&2 || true
	exit 1
fi
if ! grep -q '"windows_address": "127.0.0.1:'"$auto_windows_port"'"' "$work/auto-mappings.json" || ! grep -q '"wsl_address": "127.0.0.2:'"$auto_port"'"' "$work/auto-mappings.json"; then
	echo "automatic mapping status file did not publish the expected addresses" >&2
	cat "$work/auto-mappings.json" >&2 || true
	exit 1
fi
if ! "$windows_shell" -NoProfile -NonInteractive -Command \
	"\$response = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:$auto_windows_port' -TimeoutSec 10; if (\$response.StatusCode -ne 200) { exit 1 }; [Console]::Out.Write(\$response.Content)" \
	>"$work/auto-response.html" 2>"$work/auto-reverse.err"; then
	echo "Windows automatic mapping request failed" >&2
	cat "$work/broker.log" "$work/proxy.log" "$work/auto-http.log" "$work/auto-reverse.err" >&2 || true
	exit 1
fi
grep -q "wsl-win-relay automatic interop $endpoint" "$work/auto-response.html"
kill "$auto_http_pid" 2>/dev/null || true
wait "$auto_http_pid" 2>/dev/null || true
auto_http_pid=
for _ in $(seq 1 60); do
	if grep -q "auto-forward removed Windows port $auto_windows_port (tcp4)" "$work/proxy.log"; then
		break
	fi
	sleep 0.5
done
if ! grep -q "auto-forward removed Windows port $auto_windows_port (tcp4)" "$work/proxy.log"; then
	echo "Windows automatic mapping was not removed" >&2
	cat "$work/broker.log" "$work/proxy.log" "$work/auto-http.log" >&2 || true
	exit 1
fi
if "$windows_shell" -NoProfile -NonInteractive -Command \
	"try { Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:$auto_windows_port' -TimeoutSec 3 | Out-Null; exit 1 } catch { exit 0 }"; then
	:
else
	echo "Windows automatic mapping removal probe failed" >&2
	cat "$work/broker.log" "$work/proxy.log" >&2 || true
	exit 1
fi
if ! "$windows_shell" -NoProfile -NonInteractive -Command \
	"\$response = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:$reverse_port' -TimeoutSec 10; if (\$response.StatusCode -ne 200) { exit 1 }; [Console]::Out.Write(\$response.Content)" \
	>"$work/reverse-response.html" 2>"$work/reverse.err"; then
	echo "Windows reverse mapping request failed" >&2
	cat "$work/broker.log" "$work/proxy.log" "$work/http.log" "$work/reverse.err" >&2 || true
	exit 1
fi
grep -q "wsl-win-relay reverse interop $endpoint" "$work/reverse-response.html"
if [ "$auto_udp" -eq 1 ]; then
	echo "WSL proxies and Windows-allocated TCP/UDP mappings passed broker interop, connector recovery, and mapping cleanup"
elif [ -n "$upstream_proxy" ]; then
	echo "WSL SOCKS5 and HTTP proxies reached example.com through Windows broker and upstream $upstream_proxy; Windows reverse and automatic mappings reached WSL HTTP services"
else
	echo "WSL SOCKS5 and HTTP proxies attached to Windows broker over named pipe, reached example.com, and Windows reverse and automatic mappings reached WSL HTTP services"
fi
