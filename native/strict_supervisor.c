#define _GNU_SOURCE

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <linux/sched.h>
#include <limits.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ptrace.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/uio.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>
#include <time.h>
#include "strict_supervisor_regs.h"

#ifndef CLOSE_RANGE_UNSHARE
#define CLOSE_RANGE_UNSHARE (1U << 1)
#endif
#ifndef CLOSE_RANGE_CLOEXEC
#define CLOSE_RANGE_CLOEXEC (1U << 2)
#endif

enum pending_kind {
    PENDING_NONE,
    PENDING_SOCKET,
    PENDING_TCP_BIND,
    PENDING_UDP_BIND,
    PENDING_TCP_LISTEN,
    PENDING_CLOSE,
    PENDING_CLOSE_RANGE,
    PENDING_DUP,
    PENDING_FCNTL_DUP,
    PENDING_FCNTL_FLAGS,
    PENDING_CREATE,
    PENDING_DENY,
};

struct binding {
    int fd;
    int type;
    int family;
    char host[INET6_ADDRSTRLEN];
    uint16_t port;
    int bound;
    int close_on_exec;
    uint64_t lease;
    unsigned refs;
    struct binding *next;
};

struct pending {
    enum pending_kind kind;
    int fd;
    int oldfd;
    int newfd;
    int flags;
    uint64_t create_flags;
    unsigned range_first;
    unsigned range_last;
    int family;
    int type;
    uint64_t lease;
};

struct task_group {
    pid_t owner_pid;
    unsigned refs;
    struct binding *bindings;
};

static const char *control_path;
struct task {
    pid_t pid;
    int entering;
    int exiting;
    struct task_group *group;
    struct pending pending;
    struct task *next;
};

static pid_t root_pid;
static struct task *tasks;
static struct task *active_task;
#define pending_call (active_task->pending)

static void release_group_owner(struct task_group *group, pid_t owner);

static int debug_enabled(void) {
    const char *value = getenv("WSL_WIN_RELAY_DEBUG");
    return value != NULL && value[0] != '\0' && strcmp(value, "0") != 0;
}

static int write_all(int fd, const char *data, size_t length) {
    while (length > 0) {
        ssize_t written = write(fd, data, length);
        if (written < 0) {
            if (errno == EINTR) {
                continue;
            }
            return -1;
        }
        if (written == 0) {
            errno = EIO;
            return -1;
        }
        data += written;
        length -= (size_t)written;
    }
    return 0;
}

static int read_line(int fd, char *response, size_t capacity) {
    size_t used = 0;
    while (used + 1 < capacity) {
        char byte;
        ssize_t count = read(fd, &byte, 1);
        if (count < 0) {
            if (errno == EINTR) {
                continue;
            }
            return -1;
        }
        if (count == 0) {
            errno = ECONNRESET;
            return -1;
        }
        response[used++] = byte;
        if (byte == '\n') {
            response[used] = '\0';
            return 0;
        }
    }
    errno = EMSGSIZE;
    return -1;
}

static int control_once(const char *request, char *response, size_t capacity) {
    if (control_path == NULL || control_path[0] == '\0') {
        errno = ENOENT;
        return -1;
    }
    int fd = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (fd < 0) {
        return -1;
    }
    struct sockaddr_un address;
    memset(&address, 0, sizeof(address));
    address.sun_family = AF_UNIX;
    if (strlen(control_path) >= sizeof(address.sun_path)) {
        close(fd);
        errno = ENAMETOOLONG;
        return -1;
    }
    memcpy(address.sun_path, control_path, strlen(control_path) + 1);
    if (connect(fd, (struct sockaddr *)&address, sizeof(address)) < 0 ||
        write_all(fd, request, strlen(request)) < 0 ||
        read_line(fd, response, capacity) < 0) {
        int saved = errno;
        close(fd);
        errno = saved;
        return -1;
    }
    close(fd);
    return 0;
}

static int control_request(const char *request, char *response, size_t capacity) {
    int attempts = 21;
    const char *value = getenv("WSL_WIN_RELAY_CONTROL_RETRY_SECONDS");
    if (value != NULL && value[0] != '\0') {
        char *end = NULL;
        long seconds = strtol(value, &end, 10);
        if (end != value && *end == '\0' && seconds >= 0) {
            if (seconds > 60) {
                seconds = 60;
            }
            attempts = (int)(seconds * 10) + 1;
        }
    }
    for (int attempt = 0; attempt < attempts; attempt++) {
        if (control_once(request, response, capacity) == 0) {
            return 0;
        }
        if (errno != ENOENT && errno != ECONNREFUSED && errno != ECONNRESET && errno != ETIMEDOUT && errno != EAGAIN) {
            return -1;
        }
        if (attempt + 1 < attempts) {
            struct timespec delay = {.tv_sec = 0, .tv_nsec = 100000000L};
            nanosleep(&delay, NULL);
        }
    }
    return -1;
}

static int parse_response(const char *response, unsigned long long *value) {
    unsigned error;
    if (strncmp(response, "OK", 2) == 0) {
        if (value != NULL && sscanf(response, "OK %llu", value) != 1) {
            errno = EPROTO;
            return -1;
        }
        return 0;
    }
    if (sscanf(response, "ERR %u", &error) == 1) {
        errno = (int)error;
    } else {
        errno = EPROTO;
    }
    return -1;
}

static int reserve_lease(const char *network, const char *host, uint16_t port, uint64_t *lease) {
    char request[256];
    char response[512];
    snprintf(request, sizeof(request), "RESERVE %ld %s %u %s\n", (long)active_task->group->owner_pid, network, (unsigned)port, host);
    unsigned long long value;
    if (control_request(request, response, sizeof(response)) < 0 || parse_response(response, &value) < 0) {
        return -1;
    }
    *lease = (uint64_t)value;
    return 0;
}

static int lease_operation(const char *operation, uint64_t lease) {
    char request[128];
    char response[512];
    snprintf(request, sizeof(request), "%s %llu\n", operation, (unsigned long long)lease);
    if (control_request(request, response, sizeof(response)) < 0) {
        return -1;
    }
    return parse_response(response, NULL);
}

static int owner_lease_operation(const char *operation, pid_t pid, uint64_t lease) {
    char request[160];
    char response[512];
    snprintf(request, sizeof(request), "%s %ld %llu\n", operation, (long)pid, (unsigned long long)lease);
    if (control_request(request, response, sizeof(response)) < 0) {
        return -1;
    }
    return parse_response(response, NULL);
}

static struct binding *find_binding(int fd) {
    for (struct binding *binding = active_task->group->bindings; binding != NULL; binding = binding->next) {
        if (binding->fd == fd) {
            return binding;
        }
    }
    return NULL;
}

static struct binding *add_binding(int fd, int type, int family) {
    struct binding *binding = calloc(1, sizeof(*binding));
    if (binding == NULL) {
        return NULL;
    }
    binding->fd = fd;
    binding->type = type;
    binding->family = family;
    binding->refs = 1;
    binding->next = active_task->group->bindings;
    active_task->group->bindings = binding;
    return binding;
}

static int lease_is_referenced(uint64_t lease) {
    for (struct binding *binding = active_task->group->bindings; binding != NULL; binding = binding->next) {
        if (binding->lease == lease) {
            return 1;
        }
    }
    return 0;
}

static struct task *find_task(pid_t pid) {
    for (struct task *task = tasks; task != NULL; task = task->next) {
        if (task->pid == pid) {
            return task;
        }
    }
    return NULL;
}

static struct task_group *new_group(pid_t owner_pid) {
    struct task_group *group = calloc(1, sizeof(*group));
    if (group != NULL) {
        group->owner_pid = owner_pid;
    }
    return group;
}

static struct task *add_task(pid_t pid, struct task_group *group) {
    struct task *task = calloc(1, sizeof(*task));
    if (task == NULL) {
        return NULL;
    }
    task->pid = pid;
    task->entering = 1;
    task->group = group;
    if (group != NULL) {
        group->refs++;
    }
    task->next = tasks;
    tasks = task;
    return task;
}

static struct task_group *clone_group(struct task_group *parent, pid_t owner_pid) {
    struct task_group *group = new_group(owner_pid);
    if (group == NULL) {
        return NULL;
    }
    for (struct binding *source = parent->bindings; source != NULL; source = source->next) {
        struct binding *copy = calloc(1, sizeof(*copy));
        if (copy == NULL) {
            release_group_owner(group, owner_pid);
            free(group);
            return NULL;
        }
        *copy = *source;
        copy->next = group->bindings;
        group->bindings = copy;
        int seen = 0;
        for (struct binding *prior = parent->bindings; prior != source; prior = prior->next) {
            if (prior->lease == source->lease) {
                seen = 1;
                break;
            }
        }
        if (source->lease == 0 || seen) {
            continue;
        }
        if (owner_lease_operation("ADOPT", owner_pid, source->lease) < 0) {
            release_group_owner(group, owner_pid);
            while (group->bindings != NULL) {
                struct binding *next = group->bindings->next;
                free(group->bindings);
                group->bindings = next;
            }
            free(group);
            return NULL;
        }
    }
    return group;
}

static void release_group(struct task_group *group) {
    active_task = NULL;
    while (group->bindings != NULL) {
        struct binding *next = group->bindings->next;
        uint64_t lease = group->bindings->lease;
        int seen = 0;
        for (struct binding *prior = group->bindings->next; prior != NULL; prior = prior->next) {
            if (prior->lease == lease) {
                seen = 1;
                break;
            }
        }
        if (lease != 0 && !seen) {
            (void)owner_lease_operation("RELEASE", group->owner_pid, lease);
        }
        free(group->bindings);
        group->bindings = next;
    }
    free(group);
}

static int migrate_group_owner(struct task_group *group, pid_t new_owner) {
    if (debug_enabled()) {
        fprintf(stderr, "strict-supervisor: migrate owner %ld -> %ld\n", (long)group->owner_pid, (long)new_owner);
    }
    for (struct binding *binding = group->bindings; binding != NULL; binding = binding->next) {
        if (binding->lease == 0) {
            continue;
        }
        int seen = 0;
        for (struct binding *prior = group->bindings; prior != binding; prior = prior->next) {
            if (prior->lease == binding->lease) {
                seen = 1;
                break;
            }
        }
        if (!seen && owner_lease_operation("ADOPT", new_owner, binding->lease) < 0) {
            /* ADOPT is a batch operation. Roll back successful entries so a
             * partial owner migration cannot leave leases split across tasks. */
            release_group_owner(group, new_owner);
            return -1;
        }
    }
    return 0;
}

static void release_group_owner(struct task_group *group, pid_t owner) {
    if (debug_enabled()) {
        fprintf(stderr, "strict-supervisor: release owner %ld\n", (long)owner);
    }
    for (struct binding *binding = group->bindings; binding != NULL; binding = binding->next) {
        if (binding->lease == 0) {
            continue;
        }
        int seen = 0;
        for (struct binding *prior = group->bindings; prior != binding; prior = prior->next) {
            if (prior->lease == binding->lease) {
                seen = 1;
                break;
            }
        }
        if (!seen) {
            (void)owner_lease_operation("RELEASE", owner, binding->lease);
        }
    }
}

static void maybe_migrate_owner(struct task *task) {
    struct task_group *group = task->group;
    if (group == NULL || group->owner_pid != task->pid || group->refs <= 1) {
        return;
    }
    for (struct task *candidate = tasks; candidate != NULL; candidate = candidate->next) {
        if (candidate == task || candidate->exiting || candidate->group != group) {
            continue;
        }
        if (migrate_group_owner(group, candidate->pid) == 0) {
            group->owner_pid = candidate->pid;
            release_group_owner(group, task->pid);
        }
        return;
    }
}

static void remove_task(struct task *task) {
    struct task **cursor = &tasks;
    while (*cursor != NULL) {
        if (*cursor == task) {
            *cursor = task->next;
            struct task_group *group = task->group;
            maybe_migrate_owner(task);
            if (group != NULL && group->refs > 0) {
                group->refs--;
                if (group->refs == 0) {
                    release_group(group);
                }
            }
            free(task);
            active_task = NULL;
            return;
        }
        cursor = &(*cursor)->next;
    }
}

static void cleanup_tasks(void) {
    while (tasks != NULL) {
        remove_task(tasks);
    }
    active_task = NULL;
}

static void terminate_tracee(void) {
    if (root_pid > 0) {
        (void)kill(root_pid, SIGKILL);
    }
    for (struct task *task = tasks; task != NULL; task = task->next) {
        if (task->pid != root_pid) {
            (void)kill(task->pid, SIGKILL);
        }
    }
    for (;;) {
        int status;
        pid_t pid = waitpid(-1, &status, __WALL);
        if (pid > 0) {
            continue;
        }
        if (pid < 0 && errno == EINTR) {
            continue;
        }
        break;
    }
}

static void remove_binding(int fd) {
    struct task_group *group = active_task->group;
    struct binding **cursor = &group->bindings;
    while (*cursor != NULL) {
        if ((*cursor)->fd == fd) {
            struct binding *removed = *cursor;
            *cursor = removed->next;
            if (removed->lease != 0 && !lease_is_referenced(removed->lease)) {
                /* CLONE_THREAD tasks share one descriptor table and one
                 * lease owner. The ptrace task TID is not a valid owner. */
                (void)owner_lease_operation("RELEASE", group->owner_pid, removed->lease);
            }
            free(removed);
            return;
        }
        cursor = &(*cursor)->next;
    }
}

static void remove_bindings_range(unsigned first, unsigned last) {
    for (;;) {
        int found = -1;
        for (struct binding *binding = active_task->group->bindings; binding != NULL; binding = binding->next) {
            unsigned fd = (unsigned)binding->fd;
            if (fd >= first && fd <= last) {
                found = binding->fd;
                break;
            }
        }
        if (found < 0) {
            return;
        }
        remove_binding(found);
    }
}

static void mark_bindings_close_on_exec(unsigned first, unsigned last) {
    for (struct binding *binding = active_task->group->bindings; binding != NULL; binding = binding->next) {
        unsigned fd = (unsigned)binding->fd;
        if (fd >= first && fd <= last) {
            binding->close_on_exec = 1;
        }
    }
}

static void remove_close_on_exec_bindings(void) {
    for (;;) {
        int found = -1;
        for (struct binding *binding = active_task->group->bindings; binding != NULL; binding = binding->next) {
            if (binding->close_on_exec) {
                found = binding->fd;
                break;
            }
        }
        if (found < 0) {
            return;
        }
        remove_binding(found);
    }
}

static int read_target_memory(unsigned long address, void *buffer, size_t length) {
    struct iovec local = {.iov_base = buffer, .iov_len = length};
    struct iovec remote = {.iov_base = (void *)address, .iov_len = length};
    ssize_t copied = process_vm_readv(active_task->pid, &local, 1, &remote, 1, 0);
    if (copied < 0) {
        return -1;
    }
    if ((size_t)copied != length) {
        errno = EFAULT;
        return -1;
    }
    return 0;
}

static int decode_address(unsigned long address, unsigned long length, int *family, char *host, size_t host_capacity, uint16_t *port) {
    struct sockaddr_storage storage;
    memset(&storage, 0, sizeof(storage));
    if (length > sizeof(storage)) {
        length = sizeof(storage);
    }
    if (read_target_memory(address, &storage, length) < 0) {
        return -1;
    }
    if (storage.ss_family == AF_INET && length >= sizeof(struct sockaddr_in)) {
        const struct sockaddr_in *ipv4 = (const struct sockaddr_in *)&storage;
        *family = AF_INET;
        *port = ntohs(ipv4->sin_port);
        if (inet_ntop(AF_INET, &ipv4->sin_addr, host, host_capacity) == NULL) {
            return -1;
        }
        return 0;
    }
    if (storage.ss_family == AF_INET6 && length >= sizeof(struct sockaddr_in6)) {
        const struct sockaddr_in6 *ipv6 = (const struct sockaddr_in6 *)&storage;
        *family = AF_INET6;
        *port = ntohs(ipv6->sin6_port);
        if (inet_ntop(AF_INET6, &ipv6->sin6_addr, host, host_capacity) == NULL) {
            return -1;
        }
        return 0;
    }
    errno = EAFNOSUPPORT;
    return -1;
}

static const char *network_name(int family, int type) {
    if (family == AF_INET && type == SOCK_STREAM) return "tcp4";
    if (family == AF_INET6 && type == SOCK_STREAM) return "tcp6";
    if (family == AF_INET && type == SOCK_DGRAM) return "udp4";
    if (family == AF_INET6 && type == SOCK_DGRAM) return "udp6";
    return NULL;
}

static int stop_syscall(wwr_regs *regs, int error) {
    WWR_SYSCALL(regs) = (unsigned long)-1;
    if (wwr_set_regs(active_task->pid, regs) < 0) {
        return -1;
    }
    pending_call.kind = PENDING_DENY;
    pending_call.lease = (uint64_t)error;
    return 0;
}

static int apply_return_error(wwr_regs *regs) {
    WWR_RETURN(regs) = (unsigned long)-(long)pending_call.lease;
    if (wwr_set_regs(active_task->pid, regs) < 0) {
        return -1;
    }
    pending_call.kind = PENDING_NONE;
    return 0;
}

static int handle_entry(wwr_regs *regs) {
    pending_call.kind = PENDING_NONE;
    unsigned long syscall_number = WWR_SYSCALL(regs);
    if (debug_enabled()) {
        fprintf(stderr, "strict-supervisor: syscall %lu\n", syscall_number);
    }
    if (syscall_number == SYS_vfork) {
        pending_call.kind = PENDING_CREATE;
        pending_call.type = 0;
        return 0;
    }
    if (syscall_number == SYS_fork) {
        pending_call.kind = PENDING_CREATE;
        pending_call.type = 0;
        return 0;
    }
    if (syscall_number == SYS_clone) {
        pending_call.kind = PENDING_CREATE;
        pending_call.create_flags = (uint64_t)WWR_ARG(regs, 0);
        pending_call.type = (pending_call.create_flags & CLONE_THREAD) != 0;
        return 0;
    }
#ifdef SYS_clone3
    if (syscall_number == SYS_clone3) {
        struct clone_args arguments;
        memset(&arguments, 0, sizeof(arguments));
        if (WWR_ARG(regs, 1) < sizeof(arguments.flags) ||
            read_target_memory(WWR_ARG(regs, 0), &arguments, WWR_ARG(regs, 1) < sizeof(arguments) ? WWR_ARG(regs, 1) : sizeof(arguments)) < 0) {
            return stop_syscall(regs, ENOTSUP);
        }
        pending_call.kind = PENDING_CREATE;
        pending_call.create_flags = arguments.flags;
        pending_call.type = (arguments.flags & CLONE_THREAD) != 0;
    }
#endif
    if (syscall_number == SYS_socket) {
        pending_call.kind = PENDING_SOCKET;
        pending_call.family = (int)WWR_ARG(regs, 0);
        pending_call.type = (int)WWR_ARG(regs, 1) & 0xf;
        pending_call.flags = (int)WWR_ARG(regs, 1) & SOCK_CLOEXEC;
        return 0;
    }
    if (syscall_number == SYS_close) {
        pending_call.kind = PENDING_CLOSE;
        pending_call.fd = (int)WWR_ARG(regs, 0);
        return 0;
    }
#ifdef SYS_close_range
    if (syscall_number == SYS_close_range) {
        unsigned long first = WWR_ARG(regs, 0);
        unsigned long last = WWR_ARG(regs, 1);
        unsigned long flags = WWR_ARG(regs, 2);
        if ((flags & CLOSE_RANGE_UNSHARE) != 0 ||
            (flags & ~(unsigned long)CLOSE_RANGE_CLOEXEC) != 0 ||
            first > UINT_MAX || last > UINT_MAX) {
            return stop_syscall(regs, ENOTSUP);
        }
        pending_call.kind = PENDING_CLOSE_RANGE;
        pending_call.range_first = (unsigned)first;
        pending_call.range_last = (unsigned)last;
        pending_call.flags = (int)flags;
        return 0;
    }
#endif
    if (syscall_number == SYS_dup || syscall_number == SYS_dup2 || syscall_number == SYS_dup3) {
        pending_call.kind = PENDING_DUP;
        pending_call.oldfd = (int)WWR_ARG(regs, 0);
        pending_call.newfd = syscall_number == SYS_dup ? -1 : (int)WWR_ARG(regs, 1);
        pending_call.flags = syscall_number == SYS_dup3 ? (int)WWR_ARG(regs, 2) : 0;
        return 0;
    }
#ifdef SYS_fcntl
    if (syscall_number == SYS_fcntl) {
        int command = (int)WWR_ARG(regs, 1);
#ifdef F_DUPFD
        if (command == F_DUPFD || command == F_DUPFD_CLOEXEC) {
            pending_call.kind = PENDING_FCNTL_DUP;
            pending_call.oldfd = (int)WWR_ARG(regs, 0);
            pending_call.flags = command == F_DUPFD_CLOEXEC ? FD_CLOEXEC : 0;
            return 0;
        }
#endif
#ifdef F_SETFD
        if (command == F_SETFD) {
            pending_call.kind = PENDING_FCNTL_FLAGS;
            pending_call.fd = (int)WWR_ARG(regs, 0);
            pending_call.flags = (int)WWR_ARG(regs, 2);
            return 0;
        }
#endif
    }
#endif
    if (syscall_number == SYS_bind) {
        struct binding *binding = find_binding((int)WWR_ARG(regs, 0));
        if (binding == NULL || (binding->type != SOCK_DGRAM && binding->type != SOCK_STREAM)) {
            return 0;
        }
        int family;
        uint16_t port;
        char host[INET6_ADDRSTRLEN];
        if (decode_address(WWR_ARG(regs, 1), WWR_ARG(regs, 2), &family, host, sizeof(host), &port) < 0 || port == 0) {
            if (debug_enabled()) fprintf(stderr, "strict-supervisor: bind address decode failed\n");
            return 0;
        }
        binding->family = family;
        binding->port = port;
        snprintf(binding->host, sizeof(binding->host), "%s", host);
        if (binding->type == SOCK_STREAM) {
            pending_call.kind = PENDING_TCP_BIND;
            pending_call.fd = binding->fd;
            return 0;
        }
        const char *network = network_name(family, SOCK_DGRAM);
        if (network == NULL) {
            return 0;
        }
        uint64_t lease;
        if (reserve_lease(network, host, port, &lease) < 0) {
            return stop_syscall(regs, errno);
        }
        if (debug_enabled()) fprintf(stderr, "strict-supervisor: UDP bind %s:%u\n", host, (unsigned)port);
        pending_call.kind = PENDING_UDP_BIND;
        pending_call.fd = binding->fd;
        pending_call.lease = lease;
        return 0;
    }
    if (syscall_number == SYS_listen) {
        struct binding *binding = find_binding((int)WWR_ARG(regs, 0));
        if (debug_enabled()) {
            fprintf(stderr, "strict-supervisor: listen fd=%d binding=%p port=%u type=%d\n", (int)WWR_ARG(regs, 0),
                    (void *)binding, binding == NULL ? 0 : (unsigned)binding->port, binding == NULL ? 0 : binding->type);
        }
        if (binding == NULL || binding->type != SOCK_STREAM || !binding->bound || binding->port == 0 || binding->lease != 0) {
            return 0;
        }
        const char *network = network_name(binding->family, SOCK_STREAM);
        if (network == NULL) {
            return 0;
        }
        uint64_t lease;
        if (reserve_lease(network, binding->host, binding->port, &lease) < 0) {
            return stop_syscall(regs, errno);
        }
        pending_call.kind = PENDING_TCP_LISTEN;
        pending_call.fd = binding->fd;
        pending_call.lease = lease;
    }
    return 0;
}

static int handle_exit(wwr_regs *regs) {
    if (pending_call.kind == PENDING_NONE) {
        return 0;
    }
    if (pending_call.kind == PENDING_DENY) {
        return apply_return_error(regs);
    }
    long result = (long)WWR_RETURN(regs);
    switch (pending_call.kind) {
    case PENDING_SOCKET:
        if (result >= 0 && find_binding((int)result) == NULL) {
            if (add_binding((int)result, pending_call.type, pending_call.family) == NULL) {
                return -1;
            }
            find_binding((int)result)->close_on_exec = pending_call.flags != 0;
        }
        break;
    case PENDING_UDP_BIND: {
        struct binding *binding = find_binding(pending_call.fd);
        if (result == 0 && binding != NULL) {
            binding->lease = pending_call.lease;
        } else {
            (void)lease_operation("ABORT", pending_call.lease);
        }
        break;
    }
    case PENDING_TCP_BIND: {
        struct binding *binding = find_binding(pending_call.fd);
        if (binding != NULL) {
            binding->bound = result == 0;
            if (!binding->bound) {
                binding->port = 0;
                binding->host[0] = '\0';
            }
        }
        break;
    }
    case PENDING_TCP_LISTEN: {
        struct binding *binding = find_binding(pending_call.fd);
        if (result != 0) {
            (void)lease_operation("ABORT", pending_call.lease);
        } else if (lease_operation("COMMIT", pending_call.lease) < 0) {
            /* A committed listener cannot be rolled back atomically after the
             * target syscall. Terminate the target rather than leave a
             * listener whose Windows half is unknown. */
            kill(active_task->pid, SIGTERM);
        } else if (binding != NULL) {
            binding->lease = pending_call.lease;
        }
        break;
    }
    case PENDING_CLOSE:
        if (result == 0) {
            remove_binding(pending_call.fd);
        }
        break;
    case PENDING_CLOSE_RANGE:
        if (result == 0 && pending_call.range_first <= pending_call.range_last) {
            if ((pending_call.flags & CLOSE_RANGE_CLOEXEC) != 0) {
                mark_bindings_close_on_exec(pending_call.range_first, pending_call.range_last);
            } else {
                remove_bindings_range(pending_call.range_first, pending_call.range_last);
            }
        }
        break;
    case PENDING_DUP:
        if (result >= 0) {
            struct binding *source = find_binding(pending_call.oldfd);
            if (result != pending_call.oldfd && find_binding((int)result) != NULL) {
                remove_binding((int)result);
            }
            if (source != NULL && find_binding((int)result) == NULL) {
                struct binding *copy = add_binding((int)result, source->type, source->family);
                if (copy == NULL) return -1;
                copy->port = source->port;
                copy->bound = source->bound;
                copy->close_on_exec = (pending_call.flags & O_CLOEXEC) != 0;
                snprintf(copy->host, sizeof(copy->host), "%s", source->host);
                copy->lease = source->lease;
            }
        }
        break;
    case PENDING_FCNTL_DUP:
        if (result >= 0) {
            struct binding *source = find_binding(pending_call.oldfd);
            if (source != NULL && find_binding((int)result) == NULL) {
                struct binding *copy = add_binding((int)result, source->type, source->family);
                if (copy == NULL) return -1;
                copy->port = source->port;
                copy->bound = source->bound;
                copy->close_on_exec = (pending_call.flags & FD_CLOEXEC) != 0;
                snprintf(copy->host, sizeof(copy->host), "%s", source->host);
                copy->lease = source->lease;
            }
        }
        break;
    case PENDING_FCNTL_FLAGS: {
        struct binding *binding = find_binding(pending_call.fd);
        if (result == 0 && binding != NULL) {
            binding->close_on_exec = (pending_call.flags & FD_CLOEXEC) != 0;
        }
        break;
    }
    case PENDING_CREATE:
        break;
    default:
        break;
    }
    pending_call.kind = PENDING_NONE;
    return 0;
}

static int trace_target(void) {
    int status;
    if (waitpid(root_pid, &status, 0) < 0 || !WIFSTOPPED(status)) {
        return -1;
    }
    struct task_group *root_group = new_group(root_pid);
    struct task *root = root_group == NULL ? NULL : add_task(root_pid, root_group);
    if (root == NULL) {
        free(root_group);
        return -1;
    }
    active_task = root;
    long options = PTRACE_O_EXITKILL | PTRACE_O_TRACESYSGOOD | PTRACE_O_TRACEFORK | PTRACE_O_TRACEVFORK | PTRACE_O_TRACECLONE | PTRACE_O_TRACEEXIT | PTRACE_O_TRACEEXEC;
    if (ptrace(PTRACE_SETOPTIONS, root_pid, 0, options) < 0 ||
        ptrace(PTRACE_SYSCALL, root_pid, 0, 0) < 0) {
        return -1;
    }
    int root_status = 1;
    int root_done = 0;
    for (;;) {
        pid_t pid = waitpid(-1, &status, __WALL);
        if (pid < 0) {
            if (errno == EINTR) continue;
            if (errno == ECHILD) break;
            return -1;
        }
        struct task *task = find_task(pid);
        if (task == NULL) {
            if (debug_enabled()) {
                fprintf(stderr, "strict-supervisor: status for unknown task %ld\n", (long)pid);
            }
            return -1;
        }
        active_task = task;
        if (WIFEXITED(status)) {
            if (debug_enabled()) fprintf(stderr, "strict-supervisor: task %ld exited %d\n", (long)pid, WEXITSTATUS(status));
            if (pid == root_pid) {
                root_status = WEXITSTATUS(status);
                root_done = 1;
            }
            remove_task(task);
            if (root_done && tasks == NULL) break;
            continue;
        }
        if (WIFSIGNALED(status)) {
            if (debug_enabled()) fprintf(stderr, "strict-supervisor: task %ld signaled %d\n", (long)pid, WTERMSIG(status));
            if (pid == root_pid) {
                root_status = 128 + WTERMSIG(status);
                root_done = 1;
            }
            remove_task(task);
            if (root_done && tasks == NULL) break;
            continue;
        }
        if (!WIFSTOPPED(status)) continue;
        int signal_number = WSTOPSIG(status);
        unsigned event = (unsigned)status >> 16;
        if (signal_number == SIGTRAP && (event == PTRACE_EVENT_FORK || event == PTRACE_EVENT_VFORK || event == PTRACE_EVENT_CLONE)) {
            unsigned long child_value = 0;
            if (ptrace(PTRACE_GETEVENTMSG, pid, 0, &child_value) < 0) return -1;
            int thread_child = event == PTRACE_EVENT_CLONE && task->pending.kind == PENDING_CREATE && task->pending.type != 0;
            int shared_files = task->pending.kind == PENDING_CREATE &&
                (task->pending.create_flags & CLONE_FILES) != 0;
            int shared_group = thread_child || shared_files;
            if (debug_enabled()) fprintf(stderr, "strict-supervisor: task %ld created %ld thread=%d shared-files=%d\n", (long)pid, (long)child_value, thread_child, shared_files);
            struct task_group *child_group = shared_group ? task->group : clone_group(task->group, (pid_t)child_value);
            struct task *child = child_group == NULL ? NULL : add_task((pid_t)child_value, child_group);
            if (child == NULL) {
                if (!shared_group) free(child_group);
                return -1;
            }
            int child_ready = 1;
            if (ptrace(PTRACE_SETOPTIONS, child->pid, 0, options) < 0) {
                if (errno == ESRCH) {
                    /* A vfork child can exec/_exit before the parent event is
                     * serviced. Its pending wait status will still reap the
                     * task; the parent must be resumed either way. */
                    child_ready = 0;
                } else {
                    if (debug_enabled()) fprintf(stderr, "strict-supervisor: set child options %ld: %s\n", (long)child->pid, strerror(errno));
                    return -1;
                }
            }
            if (child_ready && ptrace(PTRACE_SYSCALL, child->pid, 0, 0) < 0) {
                if (errno != ESRCH) {
                    if (debug_enabled()) fprintf(stderr, "strict-supervisor: resume child %ld: %s\n", (long)child->pid, strerror(errno));
                    return -1;
                }
            }
            if (ptrace(PTRACE_SYSCALL, pid, 0, 0) < 0) {
                if (errno != ESRCH) {
                    if (debug_enabled()) fprintf(stderr, "strict-supervisor: resume parent %ld: %s\n", (long)pid, strerror(errno));
                    return -1;
                }
            }
            continue;
        }
        if (signal_number == SIGTRAP && event == PTRACE_EVENT_EXIT) {
            task->exiting = 1;
            maybe_migrate_owner(task);
            if (ptrace(PTRACE_SYSCALL, pid, 0, 0) < 0) return -1;
            continue;
        }
        if (signal_number == SIGTRAP && event == PTRACE_EVENT_EXEC) {
            remove_close_on_exec_bindings();
            if (ptrace(PTRACE_SYSCALL, pid, 0, 0) < 0) return -1;
            continue;
        }
        if (signal_number == (SIGTRAP | 0x80)) {
            wwr_regs regs;
            if (wwr_get_regs(pid, &regs) < 0) return -1;
            int error = task->entering ? handle_entry(&regs) : handle_exit(&regs);
            if (error < 0) return -1;
            task->entering = !task->entering;
            if (ptrace(PTRACE_SYSCALL, pid, 0, 0) < 0) return -1;
        } else {
            if (signal_number == SIGTRAP && event != 0) {
                if (debug_enabled()) {
                    fprintf(stderr, "strict-supervisor: unsupported ptrace event %u for task %ld\n", event, (long)pid);
                }
                return -1;
            }
            int deliver = signal_number == SIGTRAP ? 0 : signal_number;
            if (ptrace(PTRACE_SYSCALL, pid, 0, deliver) < 0) return -1;
        }
    }
    active_task = NULL;
    return root_status;
}

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: wsl-win-relay-strict COMMAND [ARG ...]\n");
        return 2;
    }
    control_path = getenv("WSL_WIN_RELAY_CONTROL");
    if (control_path == NULL || control_path[0] == '\0') {
        fprintf(stderr, "WSL_WIN_RELAY_CONTROL is required\n");
        return 2;
    }
    root_pid = fork();
    if (root_pid < 0) {
        perror("fork");
        return 1;
    }
    if (root_pid == 0) {
        if (ptrace(PTRACE_TRACEME, 0, 0, 0) < 0) _exit(127);
        raise(SIGSTOP);
        execvp(argv[1], &argv[1]);
        _exit(127);
    }
    int status = trace_target();
    if (status < 0) {
        terminate_tracee();
    }
    cleanup_tasks();
    return status < 0 ? 1 : status;
}
