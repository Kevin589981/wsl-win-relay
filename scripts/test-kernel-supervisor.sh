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
    '#include <fcntl.h>' \
    '#include <linux/sched.h>' \
    '#include <pthread.h>' \
    '#include <sys/syscall.h>' \
    '#include <sys/wait.h>' \
    '#include <unistd.h>' \
    'static void *thread_close(void *argument) { usleep(1000000); close(*(int *)argument); return NULL; }' \
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
    '  if (argc > 1 && strcmp(argv[1], "clone3") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47129); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    struct clone_args arguments = {0}; arguments.exit_signal = SIGCHLD;' \
    '    long child = syscall(SYS_clone3, &arguments, sizeof(arguments));' \
    '    if (child < 0) { int error = errno; close(fd); return error == ENOSYS || error == EPERM ? 77 : 7; }' \
    '    if (child == 0) { usleep(100000); close(fd); _exit(0); }' \
    '    close(fd); return waitpid((pid_t)child, 0, 0) == (pid_t)child ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "vfork") == 0) {' \
    '    pid_t child = vfork();' \
    '    if (child < 0) return 7;' \
    '    if (child == 0) {' \
    '      int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '      address.sin_family = AF_INET; address.sin_port = htons(47133); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '      if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) _exit(2);' \
    '      close(fd); _exit(0);' \
    '    }' \
    '    return waitpid(child, 0, 0) == child ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "thread") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0}; pthread_t thread;' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47130); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    if (pthread_create(&thread, NULL, thread_close, &fd) != 0) return 4;' \
    '    return pthread_join(thread, NULL) == 0 ? 0 : 5;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "leader-sys-exit") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0}; pthread_t thread;' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47132); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    if (pthread_create(&thread, NULL, thread_close, &fd) != 0) return 4;' \
    '    syscall(SYS_exit, 0); return 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "fcntl-dup") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47134); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    int alias = fcntl(fd, F_DUPFD_CLOEXEC, 10); if (alias < 0) return 4;' \
    '    close(fd); close(alias); return 0;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "close-range") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47135); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    return syscall(SYS_close_range, (unsigned int)fd, (unsigned int)fd, 0) == 0 ? 0 : 4;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "close-range-unshare") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47136); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    errno = 0; long result = syscall(SYS_close_range, (unsigned int)fd, (unsigned int)fd, 2);' \
    '    return result < 0 && errno == ENOTSUP ? 0 : 4;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "close-range-cloexec") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47137); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    if (syscall(SYS_close_range, (unsigned int)fd, (unsigned int)fd, 4) < 0) return 4;' \
    '    int second = socket(AF_INET, SOCK_STREAM, 0); errno = 0;' \
    '    int result = second < 0 ? -1 : bind(second, (struct sockaddr *)&address, sizeof(address));' \
    '    int error = errno; if (second >= 0) close(second); if (result == 0 || error != EADDRINUSE) return 5;' \
    '    execl("/proc/self/exe", argv[0], "post-exec", (char *)NULL); return 5;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "post-exec") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47137); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 6;' \
    '    close(fd); return 0;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "socket-cloexec") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47138); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    execl("/proc/self/exe", argv[0], "post-exec-socket", (char *)NULL); return 5;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "post-exec-socket") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47138); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 6;' \
    '    close(fd); return 0;' \
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
gcc -static -O2 -pthread -o "$tmp_dir/static-target" "$tmp_dir/target.c"

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

start_control "$tmp_dir/clone3.sock" "$tmp_dir/clone3.log"
set +e
WSL_WIN_RELAY_CONTROL="$tmp_dir/clone3.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" clone3
clone3_status=$?
set -e
if [ "$clone3_status" -ne 0 ] && [ "$clone3_status" -ne 77 ]; then
    echo "clone3 process target failed with status $clone3_status" >&2
    exit 1
fi
if [ "$clone3_status" -eq 0 ]; then
    grep -q 'RESERVE .* tcp4 47129' "$tmp_dir/clone3.log"
    grep -q '^ADOPT ' "$tmp_dir/clone3.log"
fi
stop_control

start_control "$tmp_dir/vfork.sock" "$tmp_dir/vfork.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/vfork.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" vfork
grep -q 'RESERVE .* tcp4 47133' "$tmp_dir/vfork.log"
grep -q '^COMMIT ' "$tmp_dir/vfork.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/vfork.log"
stop_control

start_control "$tmp_dir/thread.sock" "$tmp_dir/thread.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/thread.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" thread
grep -q 'RESERVE .* tcp4 47130' "$tmp_dir/thread.log"
grep -q '^COMMIT ' "$tmp_dir/thread.log"
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/thread.log")" -eq 1
thread_owner=$(sed -n 's/^RESERVE \([0-9][0-9]*\) .*/\1/p' "$tmp_dir/thread.log" | head -n 1)
thread_release=$(sed -n 's/^RELEASE \([0-9][0-9]*\) .*/\1/p' "$tmp_dir/thread.log" | head -n 1)
test -n "$thread_owner" && test "$thread_owner" = "$thread_release"
stop_control

start_control "$tmp_dir/fcntl-dup.sock" "$tmp_dir/fcntl-dup.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/fcntl-dup.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" fcntl-dup
grep -q 'RESERVE .* tcp4 47134' "$tmp_dir/fcntl-dup.log"
grep -q '^COMMIT ' "$tmp_dir/fcntl-dup.log"
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/fcntl-dup.log")" -eq 1
stop_control

start_control "$tmp_dir/close-range.sock" "$tmp_dir/close-range.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/close-range.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" close-range
grep -q 'RESERVE .* tcp4 47135' "$tmp_dir/close-range.log"
grep -q '^COMMIT ' "$tmp_dir/close-range.log"
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/close-range.log")" -eq 1
stop_control

start_control "$tmp_dir/close-range-unshare.sock" "$tmp_dir/close-range-unshare.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/close-range-unshare.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" close-range-unshare
grep -q 'RESERVE .* tcp4 47136' "$tmp_dir/close-range-unshare.log"
grep -q '^COMMIT ' "$tmp_dir/close-range-unshare.log"
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/close-range-unshare.log")" -eq 1
stop_control

start_control "$tmp_dir/close-range-cloexec.sock" "$tmp_dir/close-range-cloexec.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/close-range-cloexec.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" close-range-cloexec
grep -q 'RESERVE .* tcp4 47137' "$tmp_dir/close-range-cloexec.log"
test "$(grep -Ec '^RESERVE ' "$tmp_dir/close-range-cloexec.log")" -eq 1
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/close-range-cloexec.log")" -eq 1
stop_control

start_control "$tmp_dir/socket-cloexec.sock" "$tmp_dir/socket-cloexec.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/socket-cloexec.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" socket-cloexec
grep -q 'RESERVE .* tcp4 47138' "$tmp_dir/socket-cloexec.log"
grep -q 'RESERVE .* tcp4 47138' "$tmp_dir/socket-cloexec.log"
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/socket-cloexec.log")" -eq 2
stop_control

start_control "$tmp_dir/leader-sys-exit.sock" "$tmp_dir/leader-sys-exit.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/leader-sys-exit.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" leader-sys-exit
grep -q 'RESERVE .* tcp4 47132' "$tmp_dir/leader-sys-exit.log"
grep -q '^ADOPT ' "$tmp_dir/leader-sys-exit.log"
test "$(grep -Ec '^ADOPT ' "$tmp_dir/leader-sys-exit.log")" -eq 1
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/leader-sys-exit.log")" -eq 2
stop_control

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
