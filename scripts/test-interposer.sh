#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
control_socket=$tmp_dir/control.sock
request_log=$tmp_dir/requests.log
control_pid=
cleanup() {
    if [ -n "${control_pid:-}" ]; then
        kill "$control_pid" 2>/dev/null || true
        wait "$control_pid" 2>/dev/null || true
    fi
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

gcc -O2 -Wall -Wextra -Werror \
    -pthread \
    -o "$tmp_dir/interposer-smoke" "$repo_dir/native/interposer_smoke.c"
# Start the application first so the interposer's bounded retry path covers a
# relay control socket that is still coming up.
(
    sleep 0.2
    exec python3 "$repo_dir/scripts/interposer-control.py" "$control_socket" "$request_log" 47125 47130 6
) &
control_pid=$!
WSL_WIN_RELAY_CONTROL=$control_socket \
WSL_WIN_RELAY_CONTROL_RETRY_SECONDS=10 \
LD_PRELOAD="$repo_dir/lib/libwsl_win_relay_listen.so" \
    "$tmp_dir/interposer-smoke"

reserve_count=$(awk '$1 == "RESERVE" { count++ } END { print count + 0 }' "$request_log")
commit_count=$(awk '$1 == "COMMIT" { count++ } END { print count + 0 }' "$request_log")
adopt_count=$(awk '$1 == "ADOPT" { count++ } END { print count + 0 }' "$request_log")
release_count=$(awk '$1 == "RELEASE" { count++ } END { print count + 0 }' "$request_log")
[ "$reserve_count" -eq 9 ] || { echo "expected nine RESERVE requests including the delayed retry, raw syscall, pthread, and rejection paths, got $reserve_count" >&2; exit 1; }
[ "$commit_count" -eq 5 ] || { echo "expected five COMMIT requests including the delayed retry, raw, pthread, and delayed listen, got $commit_count" >&2; exit 1; }
[ "$adopt_count" -ge 2 ] || { echo "expected parent and child ADOPT, got $adopt_count" >&2; exit 1; }
[ "$release_count" -ge 9 ] && [ "$release_count" -le 10 ] || { echo "expected all TCP/UDP leases including delayed retry, clone, and pthread owners to RELEASE, got $release_count" >&2; exit 1; }
echo "native interposer TCP/UDP, raw syscall, clone, clone3, and pthread lifecycle passed"
