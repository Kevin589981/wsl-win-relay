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
    '#define _GNU_SOURCE' \
    '#include <arpa/inet.h>' \
    '#include <netinet/in.h>' \
    '#include <stdio.h>' \
    '#include <stdlib.h>' \
    '#include <string.h>' \
    '#include <sys/socket.h>' \
    '#include <errno.h>' \
    '#include <fcntl.h>' \
    '#include <linux/sched.h>' \
    '#include <limits.h>' \
    '#include <pthread.h>' \
    '#include <spawn.h>' \
    '#include <signal.h>' \
    '#include <sys/syscall.h>' \
    '#include <sys/wait.h>' \
    '#include <unistd.h>' \
    'extern char **environ;' \
    'static volatile int leader_listener_fd = -1;' \
    'static void *thread_close(void *argument) { usleep(1000000); close(*(int *)argument); return NULL; }' \
    'static void *thread_exit_group(void *argument) { (void)argument; usleep(100000); syscall(SYS_exit_group, 0); return NULL; }' \
    'static void *thread_listen_after_leader_exit(void *argument) { (void)argument; usleep(100000); int inherited = leader_listener_fd; leader_listener_fd = -1; if (inherited >= 0) close(inherited); int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0}; address.sin_family = AF_INET; address.sin_port = htons(47145); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK); if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return (void *)1; close(fd); return NULL; }' \
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
    '  if (argc > 1 && strcmp(argv[1], "daemon") == 0) {' \
    '    if (daemon(1, 1) < 0) return errno == ENOSYS ? 77 : 7;' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47152); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    close(fd); return 0;' \
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
    '  if (argc > 1 && strcmp(argv[1], "vfork-exec") == 0) {' \
    '    pid_t child = vfork();' \
    '    if (child < 0) return 7;' \
    '    if (child == 0) { execl(argv[0], argv[0], "spawn-child", (char *)NULL); _exit(127); }' \
    '    return waitpid(child, 0, 0) == child ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "vfork-execvp") == 0) {' \
    '    char executable[PATH_MAX]; if (realpath(argv[0], executable) == NULL) return 4;' \
    '    char *slash = strrchr(executable, '\''/'\''); if (slash == NULL) return 4; *slash = '\''\0'\'';' \
    '    if (setenv("PATH", executable, 1) != 0) return 4;' \
    '    pid_t child = vfork();' \
    '    if (child < 0) return 7;' \
    '    if (child == 0) { execlp("static-target", "static-target", "spawn-child", (char *)NULL); _exit(127); }' \
    '    return waitpid(child, 0, 0) == child ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "vfork-fexecve") == 0) {' \
    '    int executable_fd = open("/proc/self/exe", O_PATH | O_CLOEXEC); if (executable_fd < 0) return 4;' \
    '    pid_t child = vfork();' \
    '    if (child < 0) { close(executable_fd); return 7; }' \
    '    if (child == 0) { char *spawn_argv[] = { (char *)"static-target", (char *)"spawn-child", NULL }; fexecve(executable_fd, spawn_argv, environ); _exit(127); }' \
    '    int result = waitpid(child, 0, 0) == child ? 0 : 6; close(executable_fd); return result;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "spawn-child") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47147); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    close(fd); return 0;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "shell-child") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47153); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    close(fd); return 0;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "system") == 0) {' \
    '    char command[PATH_MAX]; if (snprintf(command, sizeof(command), "%s spawn-child", argv[0]) < 0) return 4;' \
    '    int status = system(command); return status == 0 ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "popen") == 0) {' \
    '    char command[PATH_MAX]; if (snprintf(command, sizeof(command), "%s spawn-child", argv[0]) < 0) return 4;' \
    '    FILE *stream = popen(command, "r"); if (stream == NULL) return 5;' \
    '    char buffer[64]; while (fread(buffer, 1, sizeof(buffer), stream) > 0) {}' \
    '    int status = pclose(stream); return status == 0 ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "posix-spawn") == 0) {' \
    '    pid_t child = -1; char *spawn_argv[] = { argv[0], (char *)"spawn-child", NULL };' \
    '    int spawn_error = posix_spawn(&child, argv[0], NULL, NULL, spawn_argv, environ);' \
    '    if (spawn_error != 0) return spawn_error;' \
    '    return waitpid(child, NULL, 0) == child ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "posix-spawnp") == 0) {' \
    '    char executable[PATH_MAX]; if (realpath(argv[0], executable) == NULL) return 4;' \
    '    char *slash = strrchr(executable, '\''/'\''); if (slash == NULL) return 4; *slash = '\''\0'\'';' \
    '    if (setenv("PATH", executable, 1) != 0) return 4;' \
    '    pid_t child = -1; char *spawn_argv[] = { (char *)slash + 1, (char *)"spawn-child", NULL };' \
    '    int spawn_error = posix_spawnp(&child, slash + 1, NULL, NULL, spawn_argv, environ);' \
    '    if (spawn_error != 0) return spawn_error;' \
    '    return waitpid(child, NULL, 0) == child ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "posix-spawn-close") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47148); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    posix_spawn_file_actions_t actions; if (posix_spawn_file_actions_init(&actions) != 0) return 4;' \
    '    if (posix_spawn_file_actions_addclose(&actions, fd) != 0) return 4;' \
    '    pid_t child = -1; char *spawn_argv[] = { argv[0], (char *)"spawn-child", NULL };' \
    '    int spawn_error = posix_spawn(&child, argv[0], &actions, NULL, spawn_argv, environ);' \
    '    posix_spawn_file_actions_destroy(&actions); if (spawn_error != 0) return spawn_error;' \
    '    int result = waitpid(child, NULL, 0) == child ? 0 : 6; close(fd); return result;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "posix-spawn-dup") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47149); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    posix_spawn_file_actions_t actions; if (posix_spawn_file_actions_init(&actions) != 0) return 4;' \
    '    if (posix_spawn_file_actions_adddup2(&actions, fd, 9) != 0 || posix_spawn_file_actions_addclose(&actions, fd) != 0) return 4;' \
    '    pid_t child = -1; char *spawn_argv[] = { argv[0], (char *)"spawn-child", NULL };' \
    '    int spawn_error = posix_spawn(&child, argv[0], &actions, NULL, spawn_argv, environ);' \
    '    posix_spawn_file_actions_destroy(&actions); if (spawn_error != 0) return spawn_error;' \
    '    int result = waitpid(child, NULL, 0) == child ? 0 : 6; close(fd); return result;' \
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
    '  if (argc > 1 && strcmp(argv[1], "leader-exit-thread-listen") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47146); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    leader_listener_fd = fd; pthread_t thread;' \
    '    if (pthread_create(&thread, NULL, thread_listen_after_leader_exit, NULL) != 0) return 4;' \
    '    syscall(SYS_exit, 0); return 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "signal") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47141); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    kill(getpid(), SIGTERM); pause(); return 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "thread-exit-group") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0}; pthread_t thread;' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47142); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    if (pthread_create(&thread, NULL, thread_exit_group, NULL) != 0) return 4;' \
    '    return pthread_join(thread, NULL) == 0 ? 0 : 5;' \
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
    '  if (argc > 1 && strcmp(argv[1], "clone-files-exec-child") == 0) {' \
    '    struct sockaddr_in address = {0}; address.sin_family = AF_INET; address.sin_port = htons(47150); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    int probe = socket(AF_INET, SOCK_STREAM, 0); errno = 0; int probe_result = probe < 0 ? -1 : bind(probe, (struct sockaddr *)&address, sizeof(address)); int probe_error = errno; if (probe >= 0) close(probe);' \
    '    if (probe_result == 0 || probe_error != EADDRINUSE) return 3;' \
    '    char ready = 1; if (write(10, &ready, 1) != 1 || read(9, &ready, 1) != 1) return 4;' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    close(fd); return 0;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "clone-files-exec") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0); struct sockaddr_in address = {0}; int trigger[2]; int ready[2];' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47150); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0 || pipe(trigger) < 0 || pipe(ready) < 0) return 2;' \
    '    if (dup2(trigger[0], 9) < 0 || dup2(ready[1], 10) < 0) return 4;' \
    '    long child = syscall(SYS_clone, (unsigned long)(CLONE_FILES | SIGCHLD), 0, NULL, NULL, NULL);' \
    '    if (child < 0) { int error = errno; close(fd); return error == ENOSYS || error == EPERM ? 77 : 4; }' \
    '    if (child == 0) { execl("/proc/self/exe", argv[0], "clone-files-exec-child", (char *)NULL); _exit(5); }' \
    '    char signal = 0; if (read(ready[0], &signal, 1) != 1) return 6;' \
    '    close(fd); if (write(trigger[1], &signal, 1) != 1) return 6;' \
    '    return waitpid((pid_t)child, NULL, 0) == (pid_t)child ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "clone-files") == 0) {' \
    '    int fd = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_port = htons(47139); address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' \
    '    if (fd < 0 || bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 4) < 0) return 2;' \
    '    long child = syscall(SYS_clone, (unsigned long)(CLONE_FILES | SIGCHLD), 0, NULL, NULL, NULL);' \
    '    if (child < 0) { int error = errno; close(fd); return error == ENOSYS || error == EPERM ? 77 : 4; }' \
    '    if (child == 0) { close(fd); _exit(0); }' \
    '    if (waitpid((pid_t)child, NULL, 0) != (pid_t)child) return 5;' \
    '    return close(fd) < 0 && errno == EBADF ? 0 : 6;' \
    '  }' \
    '  if (argc > 1 && strcmp(argv[1], "clone-rollback") == 0) {' \
    '    int first = socket(AF_INET, SOCK_STREAM, 0); int second = socket(AF_INET, SOCK_STREAM, 0); struct sockaddr_in address = {0};' \
    '    address.sin_family = AF_INET; address.sin_addr.s_addr = htonl(INADDR_LOOPBACK); address.sin_port = htons(47143);' \
    '    if (first < 0 || second < 0 || bind(first, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(first, 4) < 0) return 2;' \
    '    address.sin_port = htons(47144);' \
    '    if (bind(second, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(second, 4) < 0) return 3;' \
    '    long child = syscall(SYS_clone, (unsigned long)SIGCHLD, 0, NULL, NULL, NULL);' \
    '    if (child == 0) { close(first); close(second); _exit(0); }' \
    '    if (child < 0) return 4;' \
    '    return waitpid((pid_t)child, NULL, 0) == (pid_t)child ? 0 : 5;' \
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

start_control "$tmp_dir/vfork-exec.sock" "$tmp_dir/vfork-exec.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/vfork-exec.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" vfork-exec
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/vfork-exec.log"
grep -q '^COMMIT ' "$tmp_dir/vfork-exec.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/vfork-exec.log"
stop_control

start_control "$tmp_dir/vfork-execvp.sock" "$tmp_dir/vfork-execvp.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/vfork-execvp.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" vfork-execvp
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/vfork-execvp.log"
grep -q '^COMMIT ' "$tmp_dir/vfork-execvp.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/vfork-execvp.log"
stop_control

start_control "$tmp_dir/vfork-fexecve.sock" "$tmp_dir/vfork-fexecve.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/vfork-fexecve.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" vfork-fexecve
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/vfork-fexecve.log"
grep -q '^COMMIT ' "$tmp_dir/vfork-fexecve.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/vfork-fexecve.log"
stop_control

start_control "$tmp_dir/posix-spawn.sock" "$tmp_dir/posix-spawn.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/posix-spawn.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" posix-spawn
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/posix-spawn.log"
grep -q '^COMMIT ' "$tmp_dir/posix-spawn.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/posix-spawn.log"
stop_control

start_control "$tmp_dir/posix-spawnp.sock" "$tmp_dir/posix-spawnp.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/posix-spawnp.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" posix-spawnp
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/posix-spawnp.log"
grep -q '^COMMIT ' "$tmp_dir/posix-spawnp.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/posix-spawnp.log"
stop_control

start_control "$tmp_dir/posix-spawn-dup.sock" "$tmp_dir/posix-spawn-dup.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/posix-spawn-dup.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" posix-spawn-dup
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/posix-spawn-dup.log"
grep -q 'RESERVE .* tcp4 47149' "$tmp_dir/posix-spawn-dup.log"
grep -q '^ADOPT ' "$tmp_dir/posix-spawn-dup.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/posix-spawn-dup.log"
stop_control

start_control "$tmp_dir/posix-spawn-close.sock" "$tmp_dir/posix-spawn-close.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/posix-spawn-close.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" posix-spawn-close
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/posix-spawn-close.log"
grep -q 'RESERVE .* tcp4 47148' "$tmp_dir/posix-spawn-close.log"
grep -q '^ADOPT ' "$tmp_dir/posix-spawn-close.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/posix-spawn-close.log"
stop_control

start_control "$tmp_dir/system.sock" "$tmp_dir/system.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/system.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" system
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/system.log"
grep -q '^COMMIT ' "$tmp_dir/system.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/system.log"
stop_control

start_control "$tmp_dir/popen.sock" "$tmp_dir/popen.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/popen.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" popen
grep -q 'RESERVE .* tcp4 47147' "$tmp_dir/popen.log"
grep -q '^COMMIT ' "$tmp_dir/popen.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/popen.log"
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
test "$(grep -Ec '^RESERVE ' "$tmp_dir/close-range-cloexec.log")" -eq 2
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/close-range-cloexec.log")" -eq 2
stop_control

start_control "$tmp_dir/socket-cloexec.sock" "$tmp_dir/socket-cloexec.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/socket-cloexec.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" socket-cloexec
grep -q 'RESERVE .* tcp4 47138' "$tmp_dir/socket-cloexec.log"
grep -q 'RESERVE .* tcp4 47138' "$tmp_dir/socket-cloexec.log"
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/socket-cloexec.log")" -eq 2
stop_control

start_control "$tmp_dir/clone-files.sock" "$tmp_dir/clone-files.log"
set +e
WSL_WIN_RELAY_CONTROL="$tmp_dir/clone-files.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" clone-files
clone_files_status=$?
set -e
if [ "$clone_files_status" -ne 0 ] && [ "$clone_files_status" -ne 77 ]; then
    echo "clone-files target failed with status $clone_files_status" >&2
    exit 1
fi
if [ "$clone_files_status" -eq 0 ]; then
    grep -q 'RESERVE .* tcp4 47139' "$tmp_dir/clone-files.log"
    ! grep -q '^ADOPT ' "$tmp_dir/clone-files.log"
    test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/clone-files.log")" -eq 1
fi
stop_control

start_control "$tmp_dir/clone-files-exec.sock" "$tmp_dir/clone-files-exec.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/clone-files-exec.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" clone-files-exec
grep -q 'RESERVE .* tcp4 47150' "$tmp_dir/clone-files-exec.log"
test "$(grep -Ec '^RESERVE ' "$tmp_dir/clone-files-exec.log")" -eq 2
test "$(grep -Ec '^ADOPT ' "$tmp_dir/clone-files-exec.log")" -ge 1
test "$(grep -Ec '^COMMIT ' "$tmp_dir/clone-files-exec.log")" -eq 2
stop_control

export WWR_TEST_REJECT_ADOPT_AFTER=1
start_control "$tmp_dir/clone-rollback.sock" "$tmp_dir/clone-rollback.log"
set +e
WSL_WIN_RELAY_CONTROL="$tmp_dir/clone-rollback.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" clone-rollback
clone_rollback_status=$?
set -e
unset WWR_TEST_REJECT_ADOPT_AFTER
if [ "$clone_rollback_status" -eq 0 ]; then
    echo "clone rollback target unexpectedly succeeded after forced ADOPT failure" >&2
    exit 1
fi
grep -q 'RESERVE .* tcp4 47143' "$tmp_dir/clone-rollback.log"
grep -q 'RESERVE .* tcp4 47144' "$tmp_dir/clone-rollback.log"
test "$(grep -Ec '^ADOPT ' "$tmp_dir/clone-rollback.log")" -ge 2
rollback_owner=$(sed -n 's/^ADOPT \([0-9][0-9]*\) .*/\1/p' "$tmp_dir/clone-rollback.log" | head -n 1)
test -n "$rollback_owner"
grep -q "^RELEASE $rollback_owner " "$tmp_dir/clone-rollback.log"
stop_control

start_control "$tmp_dir/leader-sys-exit.sock" "$tmp_dir/leader-sys-exit.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/leader-sys-exit.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" leader-sys-exit
grep -q 'RESERVE .* tcp4 47132' "$tmp_dir/leader-sys-exit.log"
grep -q '^ADOPT ' "$tmp_dir/leader-sys-exit.log"
test "$(grep -Ec '^ADOPT ' "$tmp_dir/leader-sys-exit.log")" -eq 1
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/leader-sys-exit.log")" -eq 2
stop_control

start_control "$tmp_dir/leader-exit-thread-listen.sock" "$tmp_dir/leader-exit-thread-listen.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/leader-exit-thread-listen.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" leader-exit-thread-listen
if ! grep -q 'RESERVE .* tcp4 47145' "$tmp_dir/leader-exit-thread-listen.log"; then
    cat "$tmp_dir/leader-exit-thread-listen.log" >&2
    exit 1
fi
if ! grep -q 'RESERVE .* tcp4 47146' "$tmp_dir/leader-exit-thread-listen.log"; then
    cat "$tmp_dir/leader-exit-thread-listen.log" >&2
    exit 1
fi
if ! grep -q '^ADOPT ' "$tmp_dir/leader-exit-thread-listen.log"; then
    cat "$tmp_dir/leader-exit-thread-listen.log" >&2
    exit 1
fi
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/leader-exit-thread-listen.log")" -ge 2
stop_control

start_control "$tmp_dir/signal.sock" "$tmp_dir/signal.log"
set +e
WSL_WIN_RELAY_CONTROL="$tmp_dir/signal.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" signal
signal_status=$?
set -e
if [ "$signal_status" -ne 143 ]; then
    echo "signal target failed with status $signal_status" >&2
    exit 1
fi
grep -q 'RESERVE .* tcp4 47141' "$tmp_dir/signal.log"
test "$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/signal.log")" -eq 1
stop_control

start_control "$tmp_dir/thread-exit-group.sock" "$tmp_dir/thread-exit-group.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/thread-exit-group.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" thread-exit-group
grep -q 'RESERVE .* tcp4 47142' "$tmp_dir/thread-exit-group.log"
thread_exit_group_cleanup=$(grep -Ec '^(CLOSE|RELEASE) ' "$tmp_dir/thread-exit-group.log")
test "$thread_exit_group_cleanup" -ge 1
test "$thread_exit_group_cleanup" -le 2
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

start_control "$tmp_dir/daemon.sock" "$tmp_dir/daemon.log"
WSL_WIN_RELAY_CONTROL="$tmp_dir/daemon.sock" "$repo_dir/scripts/wsl-win-relay-run" --kernel "$tmp_dir/static-target" daemon
grep -q 'RESERVE .* tcp4 47152' "$tmp_dir/daemon.log"
grep -q '^COMMIT ' "$tmp_dir/daemon.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/daemon.log"
stop_control

start_control "$tmp_dir/shell.sock" "$tmp_dir/shell.log"
shell_command=$(printf '%s shell-child' "$tmp_dir/static-target")
WSL_WIN_RELAY_CONTROL="$tmp_dir/shell.sock" WSL_WIN_RELAY_SHELL=/bin/sh \
    "$repo_dir/scripts/wsl-win-relay-shell" -c "$shell_command"
grep -q 'RESERVE .* tcp4 47153' "$tmp_dir/shell.log"
grep -q '^COMMIT ' "$tmp_dir/shell.log"
grep -Eq '^(CLOSE|RELEASE) ' "$tmp_dir/shell.log"
stop_control
echo "kernel supervisor coordinated static TCP/UDP, vfork exec paths, and propagated rejection"
