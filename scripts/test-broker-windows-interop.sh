#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
proxy_bin=${WWR_PROXY_BIN:-"$repo_dir/bin/wsl-proxy-linux"}
broker_exe=${WWR_BROKER_EXE:-"$repo_dir/bin/wsl-win-broker.exe"}
connector_exe=${WWR_CONNECTOR_EXE:-"$repo_dir/bin/wsl-win-connector.exe"}
socks_listen=${WWR_BROKER_INTEROP_LISTEN:-127.0.0.1:11087}
endpoint=${WWR_BROKER_INTEROP_ENDPOINT:-"wsl-win-relay-interop-$$"}
upstream_proxy=${WWR_WINDOWS_UPSTREAM_PROXY:-}
windows_shell=${WWR_WINDOWS_SHELL:-powershell.exe}
work=$(mktemp -d "${TMPDIR:-/tmp}/wsl-win-relay-broker-interop.XXXXXX")
broker_pid=
proxy_pid=

cleanup() {
	stop_windows_roles || true
	[ -z "${proxy_pid:-}" ] || kill "$proxy_pid" 2>/dev/null || true
	[ -z "${broker_pid:-}" ] || kill "$broker_pid" 2>/dev/null || true
	[ -z "${proxy_pid:-}" ] || wait "$proxy_pid" 2>/dev/null || true
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
if [ ! -x "$windows_shell" ] && ! command -v "$windows_shell" >/dev/null 2>&1; then
	echo "Windows PowerShell is required for broker cleanup: $windows_shell (set WWR_WINDOWS_SHELL)" >&2
	exit 1
fi

token=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
start_broker() {
	if [ -n "$upstream_proxy" ]; then
		"$broker_exe" -endpoint "$endpoint" -token-hex "$token" -upstream-proxy "$upstream_proxy"
		return
	fi
	"$broker_exe" -endpoint "$endpoint" -token-hex "$token"
}
start_broker >"$work/broker.log" 2>&1 &
broker_pid=$!
sleep 1

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
	"$proxy_bin" -broker-mode -relay-exe "$connector_exe" -listen "$socks_listen" >"$work/proxy.log" 2>&1 &
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
if [ -n "$upstream_proxy" ]; then
	echo "WSL proxy reached example.com through Windows broker and upstream $upstream_proxy"
else
	echo "WSL proxy attached to Windows broker over named pipe and reached example.com"
fi
