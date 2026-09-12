#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
proxy_bin=${WWR_PROXY_BIN:-"$repo_dir/bin/wsl-proxy-linux"}
relay_exe=${WWR_RELAY_EXE:-"$repo_dir/bin/wsl-win-relay.exe"}
port=${WWR_AUTO_TEST_PORT:-47140}
proxy_listen=${WWR_AUTO_TEST_PROXY_LISTEN:-127.0.0.1:11086}
work=$(mktemp -d "${TMPDIR:-/tmp}/wsl-win-relay-auto-rebind.XXXXXX")
proxy_pid=
server_pid=
relay_pid=

cleanup() {
	if [ -n "$relay_pid" ]; then
		kill "$relay_pid" 2>/dev/null || true
	fi
	if [ -n "$proxy_pid" ]; then
		kill "$proxy_pid" 2>/dev/null || true
	fi
	if [ -n "$server_pid" ]; then
		kill "$server_pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

if [ ! -x "$proxy_bin" ]; then
	echo "missing executable proxy: $proxy_bin (run scripts/build-wsl.sh)" >&2
	exit 1
fi
if [ ! -f "$relay_exe" ]; then
	echo "missing Windows relay executable: $relay_exe (build the Windows binary)" >&2
	exit 1
fi
if ! command -v powershell.exe >/dev/null 2>&1; then
	echo "powershell.exe is required for the Windows-side probe (enable WSL interop)" >&2
	exit 1
fi

python3 -m http.server "$port" --bind 127.0.0.1 >"$work/http.log" 2>&1 &
server_pid=$!
"$proxy_bin" -relay-exe "$relay_exe" -listen "$proxy_listen" \
	-auto-forward -auto-forward-include "$port" -auto-forward-interval 100ms \
	>"$work/proxy.log" 2>&1 &
proxy_pid=$!

probe_windows_port() {
	powershell.exe -NoProfile -NonInteractive -Command \
		"try { [System.Net.Sockets.TcpClient]::new('127.0.0.1',$port).Close(); exit 0 } catch { exit 1 }" \
		>/dev/null 2>&1
}

wait_for_mapping() {
	tries=$1
	while [ "$tries" -gt 0 ]; do
		if probe_windows_port; then
			return 0
		fi
		tries=$((tries - 1))
		sleep 0.1
	done
	return 1
}

if ! wait_for_mapping 150; then
	echo "automatic Windows mapping did not become reachable" >&2
	cat "$work/proxy.log" >&2 || true
	exit 1
fi

relay_pid=$(pgrep -f 'wsl-win-relay\.exe' | head -n 1 || true)
if [ -z "$relay_pid" ]; then
	echo "could not locate the Windows relay child" >&2
	cat "$work/proxy.log" >&2 || true
	exit 1
fi
kill -9 "$relay_pid"
relay_pid=

if ! wait_for_mapping 450; then
	echo "automatic Windows mapping did not recover after relay restart" >&2
	cat "$work/proxy.log" >&2 || true
	exit 1
fi

echo "automatic Windows mapping recovered after relay child restart"
grep -E "Windows relay exited|Windows relay ready" "$work/proxy.log" || true
