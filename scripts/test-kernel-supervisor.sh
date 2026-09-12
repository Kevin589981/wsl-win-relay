#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
control_pid=
cleanup() {
    [ -z "${control_pid:-}" ] || kill "$control_pid" 2>/dev/null || true
    [ -z "${control_pid:-}" ] || wait "$control_pid" 2>/dev/null || true
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

if ! command -v gcc >/dev/null 2>&1; then
    echo "kernel supervisor test skipped: gcc unavailable"
    exit 0
fi

printf '%s\n' \
    '#include <arpa/inet.h>' \
    '#include <netinet/in.h>' \
    '#include <stdio.h>' \
    '#include <stdlib.h>' \
    '#include <string.h>' \
    '#include <sys/socket.h>' \
    '#include <errno.h>' \
    '#include <sys/wait.h>' \
    '#include <unistd.h>' \
    'int main(int argc, char **argv) {' \
    '  if (argc > 1 && strcmp(argv[1], "env") == 0) return getenv("LD_PRELOAD") == NULL ? 0 : 8;' \
    '  if (argc > 1 && strcmp(argv[1], "fork") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47128); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    pid_t child = fork();' \
    '    if (child < 0) return errno == ENOTSUP ? 0 : 7;' \
    '    if (child == 0) { usleep(100000); close(fd); _exit(0); }' \
    '    close(fd); return waitpid(child, 0, 0) == child ? 0 : 6;' \
    '  }' \
    '  int duplicate = argc > 1 && strcmp(argv[1], "dup") == 0;' \
    '  int udp = argc > 1 && strcmp(argv[1], "udp") == 0;' \
    '  int ipv6 = argc > 1 && strcmp(argv[1], "tcp6") == 0;' \
    '  int port = argc > 2 ? atoi(argv[2]) : (udp ? 47126 : (ipv6 ? 47127 : 47125));' \
    '  int family = ipv6 ? AF_INET6 : AF_INET;' \
    '  int fd = socket(family, udp ? SOCK_DGRAM : SOCK_STREAM, 0);' \
    '  struct sockaddr_storage address = {0};' \
    '  if (ipv6) { struct sockaddr_in6 *v6 = (struct sockaddr_in6 *)&address; v6->sin6_family = AF_INET6; v6->sin6_port = htons((unsigned short)port); v6->sin6_addr = in6addr_loopback; }' \
    '  else { struct sockaddr_in *v4 = (struct sockaddr_in *)&address; v4->sin_family = AF_INET; v4->sin_port = htons((unsigned short)port); v4->sin_addr.s_addr = htonl(INADDR_LOOPBACK); }' \
    '  if (fd < 0 || bind(fd, (struct sockaddr *)&address, ipv6 ? sizeof(struct sockaddr_in6) : sizeof(struct sockaddr_in)) < 0) return 2;' \
    '  if (!udp && listen(fd, 4) < 0) return 3;' \
    '  if (duplicate) { int alias = dup(fd); if (alias < 0) return 4; close(fd); usleep(100000); close(alias); return 0; }' \
    '  usleep(100000); close(fd); return 0;' \
    '}' >"$tmp_dir/target.c"
gcc -static -O2 -o "$tmp_dir/static-target" "$tmp_dir/target.c"

start_control() {
    socket_path=$1
    log_path=$2
    reject_port=${3:-}
    rm -f "$socket_path" "$log_path"
    if [ -n "$reject_port" ]; then
        python3 "$repo_dir/scripts/interposer-control.py" "$socket_path" "$log_path" "$reject_port" >"$tmp_dir/control.out" 2>&1 &
    else
        python3 "$repo_dir/scripts/interposer-control.py" "$socket_path" "$log_path" >"$tmp_dir/control.out" 2>&1 &
    fi
    control_pid=$!
    for _ in $(seq 1 50); do
        [ -S "$socket_path" ] && return 0
        sleep 0.1
    done
    cat "$tmp_dir/control.out" >&2
    return 1
}

stop_control() {
    [ -z "${control_pid:-}" ] || kill "$control_pid" 2>/dev/null || true
    [ -z "${control_pid:-}" ] || wait "$control_pid" 2>/dev/null || true
    control_pid=
}

start_control "$tmp_dir/tcp.sock" "$tmp_dir/tcp.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/tcp.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target"
grep -q 'RESERVE .* tcp4 47125' "$tmp_dir/tcp.log"
grep -q '^COMMIT ' "$tmp_dir/tcp.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/tcp.log"
stop_control

start_control "$tmp_dir/udp.sock" "$tmp_dir/udp.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/udp.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" udp
grep -q 'RESERVE .* udp4 47126' "$tmp_dir/udp.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/udp.log"
stop_control

start_control "$tmp_dir/tcp6.sock" "$tmp_dir/tcp6.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/tcp6.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" tcp6
grep -q 'RESERVE .* tcp6 47127' "$tmp_dir/tcp6.log"
grep -q '^COMMIT ' "$tmp_dir/tcp6.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/tcp6.log"
stop_control

start_control "$tmp_dir/dup.sock" "$tmp_dir/dup.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/dup.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" dup
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/dup.log")" -eq 1
stop_control

start_control "$tmp_dir/reject.sock" "$tmp_dir/reject.log" 47125
if WSL_WIN_RELAY_CONTROL="$tmp_dir/reject.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target"; then
    echo "static target unexpectedly listened after Windows rejection" >&2
    exit 1
fi
grep -q 'RESERVE .* tcp4 47125' "$tmp_dir/reject.log"
! grep -q '^COMMIT ' "$tmp_dir/reject.log"

start_control "$tmp_dir/env.sock" "$tmp_dir/env.log"
LD_PRELOAD=/definitely/not-loaded WSL_WIN_RELAY_CONTROL="$tmp_dir/env.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" env
stop_control

start_control "$tmp_dir/fork.sock" "$tmp_dir/fork.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/fork.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" fork
grep -q 'RESERVE .* tcp4 47128' "$tmp_dir/fork.log"
grep -q '^ADOPT ' "$tmp_dir/fork.log"
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/fork.log")" -eq 2
stop_control
echo "kernel supervisor coordinated static TCP/UDP and propagated rejection"
