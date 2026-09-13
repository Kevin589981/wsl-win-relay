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
missing_reserve_targets=$(awk '$1 == "RESERVE" && NF < 6 { count++ } END { print count + 0 }' "$request_log")
missing_adopt_targets=$(awk '$1 == "ADOPT" && NF < 4 { count++ } END { print count + 0 }' "$request_log")
[ "$reserve_count" -eq 9 ] || { echo "expected nine RESERVE requests including the delayed retry, raw syscall, pthread, and rejection paths, got $reserve_count" >&2; exit 1; }
[ "$commit_count" -eq 5 ] || { echo "expected five COMMIT requests including the delayed retry, raw, pthread, and delayed listen, got $commit_count" >&2; exit 1; }
[ "$adopt_count" -ge 2 ] || { echo "expected fork and clone ADOPT, got $adopt_count" >&2; exit 1; }
[ "$release_count" -ge 9 ] && [ "$release_count" -le 10 ] || { echo "expected all TCP/UDP leases including delayed retry, clone, and pthread owners to RELEASE, got $release_count" >&2; exit 1; }
[ "$missing_reserve_targets" -eq 0 ] || { echo "expected every dynamic RESERVE to carry a socket inode target, got $missing_reserve_targets missing" >&2; exit 1; }
[ "$missing_adopt_targets" -eq 0 ] || { echo "expected every dynamic ADOPT to carry a socket inode target, got $missing_adopt_targets missing" >&2; exit 1; }

# Adoption failure must fail closed: the fork caller sees an error and the
# child exits before it can run user code with an unowned listener.
kill "$control_pid" 2>/dev/null || true
wait "$control_pid" 2>/dev/null || true
control_pid=
reject_socket=$tmp_dir/reject-adopt.sock
reject_log=$tmp_dir/reject-adopt.log
(
    WWR_TEST_REJECT_ALL_ADOPT=1 \
        exec python3 "$repo_dir/scripts/interposer-control.py" "$reject_socket" "$reject_log"
) &
control_pid=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -S "$reject_socket" ] && break
    sleep 0.1
done
[ -S "$reject_socket" ] || { echo "adoption rejection control socket did not appear" >&2; exit 1; }
WSL_WIN_RELAY_CONTROL="$reject_socket" \
WSL_WIN_RELAY_CONTROL_RETRY_SECONDS=1 \
LD_PRELOAD="$repo_dir/lib/libwsl_win_relay_listen.so" \
    "$tmp_dir/interposer-smoke" adopt-failure
grep -q 'RESERVE .* tcp4 47131' "$reject_log"
grep -q '^COMMIT ' "$reject_log"
grep -q '^ADOPT ' "$reject_log"
test "$(grep -Ec '^RELEASE ' "$reject_log")" -eq 1
echo "native interposer TCP/UDP, raw syscall, clone, clone3, optional vfork, pthread, and fail-closed adoption lifecycle passed"
