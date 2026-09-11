#define _GNU_SOURCE

#include <arpa/inet.h>
#include <dlfcn.h>
#include <errno.h>
#include <netinet/in.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/un.h>
#include <sys/types.h>
#include <unistd.h>

typedef int (*listen_fn)(int, int);
typedef int (*close_fn)(int);
typedef int (*dup_fn)(int);
typedef int (*dup2_fn)(int, int);
typedef int (*dup3_fn)(int, int, int);
typedef pid_t (*fork_fn)(void);
typedef int (*close_range_fn)(unsigned int, unsigned int, int);

struct tracked_lease { uint64_t id; unsigned refs; };

struct tracked_fd {
    int fd;
    struct tracked_lease *lease;
    struct tracked_fd *next;
};

static listen_fn real_listen;
static close_fn real_close;
static dup_fn real_dup;
static dup2_fn real_dup2;
static dup3_fn real_dup3;
static fork_fn real_fork;
static close_range_fn real_close_range;
static pthread_once_t init_once = PTHREAD_ONCE_INIT;
static pthread_mutex_t tracked_mu = PTHREAD_MUTEX_INITIALIZER;
static struct tracked_fd *tracked;

static void atfork_prepare(void);
static void atfork_parent(void);
static void atfork_child(void);
static int release_lease(pid_t pid, uint64_t lease);

static int debug_enabled(void) {
    const char *value = getenv("WSL_WIN_RELAY_DEBUG");
    return value != NULL && value[0] != '\0' && strcmp(value, "0") != 0;
}

static void initialize(void) {
    real_listen = (listen_fn)dlsym(RTLD_NEXT, "listen");
    real_close = (close_fn)dlsym(RTLD_NEXT, "close");
    real_dup = (dup_fn)dlsym(RTLD_NEXT, "dup");
    real_dup2 = (dup2_fn)dlsym(RTLD_NEXT, "dup2");
    real_dup3 = (dup3_fn)dlsym(RTLD_NEXT, "dup3");
    real_fork = (fork_fn)dlsym(RTLD_NEXT, "fork");
    real_close_range = (close_range_fn)dlsym(RTLD_NEXT, "close_range");
    (void)pthread_atfork(atfork_prepare, atfork_parent, atfork_child);
}

static int write_all(int fd, const char *data, size_t length) {
    while (length > 0) {
        ssize_t written = send(fd, data, length, MSG_NOSIGNAL);
        if (written < 0) {
            return -1;
        }
        data += written;
        length -= (size_t)written;
    }
    return 0;
}

static int control_request(const char *request, char *response, size_t capacity) {
    const char *path = getenv("WSL_WIN_RELAY_CONTROL");
    if (path == NULL || path[0] == '\0') {
        errno = ENOENT;
        return -1;
    }
    if (strlen(path) >= sizeof(((struct sockaddr_un *)0)->sun_path)) {
        errno = ENAMETOOLONG;
        return -1;
    }
    int fd = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (fd < 0) {
        return -1;
    }
    struct timeval timeout = {.tv_sec = 5, .tv_usec = 0};
    (void)setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout));
    (void)setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, sizeof(timeout));
    struct sockaddr_un address;
    memset(&address, 0, sizeof(address));
    address.sun_family = AF_UNIX;
    memcpy(address.sun_path, path, strlen(path) + 1);
    if (connect(fd, (struct sockaddr *)&address, sizeof(address)) < 0 ||
        write_all(fd, request, strlen(request)) < 0) {
        int saved = errno;
        real_close(fd);
        errno = saved;
        return -1;
    }
    size_t used = 0;
    while (used + 1 < capacity) {
        ssize_t count = recv(fd, response + used, 1, 0);
        if (count <= 0) {
            int saved = count == 0 ? ECONNRESET : errno;
            real_close(fd);
            errno = saved;
            return -1;
        }
        if (response[used++] == '\n') {
            break;
        }
    }
    response[used] = '\0';
    real_close(fd);
    if (debug_enabled()) {
        fprintf(stderr, "wsl-win-relay interposer: %s=> %s", request, response);
    }
    if (strncmp(response, "ERR ", 4) == 0) {
        int remote_errno = atoi(response + 4);
        errno = remote_errno > 0 ? remote_errno : EIO;
        return -1;
    }
    if (strncmp(response, "OK", 2) != 0) {
        errno = EPROTO;
        return -1;
    }
    return 0;
}

static int reserve_listener(const char *network, uint16_t port, const char *host, uint64_t *lease) {
    char request[128];
    char response[256];
    snprintf(request, sizeof(request), "RESERVE %ld %s %u %s\n", (long)getpid(), network, (unsigned)port, host);
    if (control_request(request, response, sizeof(response)) < 0) {
        return -1;
    }
    unsigned long long value;
    if (sscanf(response, "OK %llu", &value) != 1) {
        errno = EPROTO;
        return -1;
    }
    *lease = (uint64_t)value;
    return 0;
}

static int lease_operation(const char *operation, uint64_t lease) {
    char request[128];
    char response[256];
    snprintf(request, sizeof(request), "%s %llu\n", operation, (unsigned long long)lease);
    return control_request(request, response, sizeof(response));
}

static int adopt_lease(pid_t pid, uint64_t lease) {
    char request[128];
    char response[256];
    snprintf(request, sizeof(request), "ADOPT %ld %llu\n", (long)pid, (unsigned long long)lease);
    return control_request(request, response, sizeof(response));
}

static int release_lease(pid_t pid, uint64_t lease) {
    char request[128];
    char response[256];
    snprintf(request, sizeof(request), "RELEASE %ld %llu\n", (long)pid, (unsigned long long)lease);
    return control_request(request, response, sizeof(response));
}

static void atfork_prepare(void) {
    pthread_mutex_lock(&tracked_mu);
}

static void atfork_parent(void) {
    pthread_mutex_unlock(&tracked_mu);
}

static void adopt_tracked_for_pid(pid_t pid) {
    pthread_mutex_lock(&tracked_mu);
    for (struct tracked_fd *entry = tracked; entry != NULL; entry = entry->next) {
        int seen = 0;
        for (struct tracked_fd *prior = tracked; prior != entry; prior = prior->next) {
            if (prior->lease == entry->lease) {
                seen = 1;
                break;
            }
        }
        if (!seen && adopt_lease(pid, entry->lease->id) < 0 && debug_enabled()) {
            fprintf(stderr, "wsl-win-relay interposer: ADOPT failed for lease %llu: %s\n",
                    (unsigned long long)entry->lease->id, strerror(errno));
        }
    }
    pthread_mutex_unlock(&tracked_mu);
}

static void atfork_child(void) {
    pthread_mutex_unlock(&tracked_mu);
    adopt_tracked_for_pid(getpid());
}

static int find_tracked(int fd) {
    int found = 0;
    pthread_mutex_lock(&tracked_mu);
    for (struct tracked_fd *entry = tracked; entry != NULL; entry = entry->next) {
        if (entry->fd == fd) {
            found = 1;
            break;
        }
    }
    pthread_mutex_unlock(&tracked_mu);
    return found;
}

static int add_tracked(int fd, uint64_t lease) {
	struct tracked_fd *entry = malloc(sizeof(*entry));
	if (entry == NULL) {
		return -1;
    }
    entry->fd = fd;
    entry->lease = malloc(sizeof(*entry->lease));
    if (entry->lease == NULL) { free(entry); return -1; }
    entry->lease->id = lease;
    entry->lease->refs = 1;
    pthread_mutex_lock(&tracked_mu);
    entry->next = tracked;
	tracked = entry;
	pthread_mutex_unlock(&tracked_mu);
	return 0;
}

static uint64_t remove_tracked(int fd) {
    uint64_t lease = 0;
    pthread_mutex_lock(&tracked_mu);
    struct tracked_fd **cursor = &tracked;
    while (*cursor != NULL) {
        if ((*cursor)->fd == fd) {
            struct tracked_fd *removed = *cursor;
            *cursor = removed->next;
            if (--removed->lease->refs == 0) { lease = removed->lease->id; free(removed->lease); }
            free(removed);
            break;
        }
        cursor = &(*cursor)->next;
    }
    pthread_mutex_unlock(&tracked_mu);
    return lease;
}

static void release_tracked_range(unsigned int first, unsigned int last) {
    if (first > last) {
        return;
    }
    for (;;) {
        uint64_t lease = 0;
        int found = 0;
        pthread_mutex_lock(&tracked_mu);
        struct tracked_fd **cursor = &tracked;
        while (*cursor != NULL) {
            unsigned int fd = (unsigned int)(*cursor)->fd;
            if (fd >= first && fd <= last) {
                struct tracked_fd *removed = *cursor;
                *cursor = removed->next;
                if (--removed->lease->refs == 0) {
                    lease = removed->lease->id;
                    free(removed->lease);
                }
                free(removed);
                found = 1;
                break;
            }
            cursor = &(*cursor)->next;
        }
        pthread_mutex_unlock(&tracked_mu);
        if (!found) {
            return;
        }
        if (lease != 0) {
            (void)release_lease(getpid(), lease);
        }
    }
}

static int duplicate_tracking(int oldfd, int newfd) {
    int result = 0;
    pthread_mutex_lock(&tracked_mu);
    struct tracked_fd *source = NULL;
    for (struct tracked_fd *entry = tracked; entry != NULL; entry = entry->next) {
        if (entry->fd == oldfd) { source = entry; break; }
    }
    if (source != NULL) {
        struct tracked_fd *entry = malloc(sizeof(*entry));
        if (entry == NULL) { result = -1; }
        else {
            entry->fd = newfd;
            entry->lease = source->lease;
            entry->lease->refs++;
            entry->next = tracked;
            tracked = entry;
        }
    }
    pthread_mutex_unlock(&tracked_mu);
    return result;
}

static int replace_tracking(int oldfd, int newfd, uint64_t *release) {
    *release = 0;
    struct tracked_fd *replacement = NULL;
    pthread_mutex_lock(&tracked_mu);
    struct tracked_fd *source = NULL;
    for (struct tracked_fd *entry = tracked; entry != NULL; entry = entry->next) {
        if (entry->fd == oldfd) { source = entry; break; }
    }
    struct tracked_fd **cursor = &tracked;
    while (*cursor != NULL) {
        if ((*cursor)->fd == newfd) {
            struct tracked_fd *removed = *cursor;
            *cursor = removed->next;
            if (--removed->lease->refs == 0) { *release = removed->lease->id; free(removed->lease); }
            free(removed);
            break;
        }
        cursor = &(*cursor)->next;
    }
    if (source != NULL) {
        replacement = malloc(sizeof(*replacement));
        if (replacement == NULL) {
            pthread_mutex_unlock(&tracked_mu);
            return -1;
        }
        replacement->fd = newfd;
        replacement->lease = source->lease;
        replacement->lease->refs++;
        replacement->next = tracked;
        tracked = replacement;
    }
    pthread_mutex_unlock(&tracked_mu);
    return 0;
}

int listen(int sockfd, int backlog) {
    pthread_once(&init_once, initialize);
    if (real_listen == NULL || find_tracked(sockfd)) {
        return real_listen == NULL ? (errno = ENOSYS, -1) : real_listen(sockfd, backlog);
    }
    int socket_type = 0;
    socklen_t type_length = sizeof(socket_type);
    if (getsockopt(sockfd, SOL_SOCKET, SO_TYPE, &socket_type, &type_length) < 0 || socket_type != SOCK_STREAM) {
        return real_listen(sockfd, backlog);
    }
    struct sockaddr_storage local;
    socklen_t local_length = sizeof(local);
    if (getsockname(sockfd, (struct sockaddr *)&local, &local_length) < 0) {
        return real_listen(sockfd, backlog);
    }
	const char *network;
	char host[INET6_ADDRSTRLEN];
	uint16_t port;
	if (local.ss_family == AF_INET) {
		network = "tcp4";
		port = ntohs(((struct sockaddr_in *)&local)->sin_port);
		if (inet_ntop(AF_INET, &((struct sockaddr_in *)&local)->sin_addr, host, sizeof(host)) == NULL) { return real_listen(sockfd, backlog); }
	} else if (local.ss_family == AF_INET6) {
		network = "tcp6";
		port = ntohs(((struct sockaddr_in6 *)&local)->sin6_port);
		if (inet_ntop(AF_INET6, &((struct sockaddr_in6 *)&local)->sin6_addr, host, sizeof(host)) == NULL) { return real_listen(sockfd, backlog); }
    } else {
        return real_listen(sockfd, backlog);
    }
    if (port == 0 || getenv("WSL_WIN_RELAY_CONTROL") == NULL) {
        return real_listen(sockfd, backlog);
    }
    uint64_t lease;
	if (reserve_listener(network, port, host, &lease) < 0) {
        return -1;
    }
    if (real_listen(sockfd, backlog) < 0) {
        int saved = errno;
        (void)lease_operation("ABORT", lease);
        errno = saved;
        return -1;
    }
    if (lease_operation("COMMIT", lease) < 0) {
        int saved = errno;
        (void)lease_operation("ABORT", lease);
        (void)real_close(sockfd);
        errno = saved;
        return -1;
    }
	if (add_tracked(sockfd, lease) < 0) {
		(void)lease_operation("CLOSE", lease);
		(void)real_close(sockfd);
		errno = ENOMEM;
		return -1;
	}
    return 0;
}

int close(int fd) {
    pthread_once(&init_once, initialize);
    uint64_t lease = remove_tracked(fd);
    if (lease != 0) {
        int saved = errno;
        (void)release_lease(getpid(), lease);
        errno = saved;
    }
    return real_close == NULL ? (errno = ENOSYS, -1) : real_close(fd);
}

int dup(int oldfd) {
    pthread_once(&init_once, initialize);
    int newfd = real_dup == NULL ? -1 : real_dup(oldfd);
    if (newfd >= 0 && duplicate_tracking(oldfd, newfd) < 0) {
        (void)real_close(newfd);
        errno = ENOMEM;
        return -1;
    }
    return newfd;
}

int dup2(int oldfd, int newfd) {
    pthread_once(&init_once, initialize);
    if (real_dup2 == NULL) { errno = ENOSYS; return -1; }
    int result = real_dup2(oldfd, newfd);
    if (result < 0 || oldfd == newfd) return result;
    uint64_t release = 0;
    if (replace_tracking(oldfd, newfd, &release) < 0) {
        if (release != 0) (void)lease_operation("CLOSE", release);
        (void)real_close(result);
        errno = ENOMEM;
        return -1;
    }
    if (release != 0) (void)lease_operation("CLOSE", release);
    return result;
}

int dup3(int oldfd, int newfd, int flags) {
    pthread_once(&init_once, initialize);
    if (real_dup3 == NULL) { errno = ENOSYS; return -1; }
    int result = real_dup3(oldfd, newfd, flags);
    if (result < 0 || oldfd == newfd) return result;
    uint64_t release = 0;
    if (replace_tracking(oldfd, newfd, &release) < 0) {
        if (release != 0) (void)lease_operation("CLOSE", release);
        (void)real_close(result);
        errno = ENOMEM;
        return -1;
    }
    if (release != 0) (void)lease_operation("CLOSE", release);
    return result;
}

pid_t fork(void) {
    pthread_once(&init_once, initialize);
    if (real_fork == NULL) {
        errno = ENOSYS;
        return -1;
    }
    pid_t child = real_fork();
    if (child > 0) {
        adopt_tracked_for_pid(child);
    }
    return child;
}

#ifndef CLOSE_RANGE_CLOEXEC
#define CLOSE_RANGE_CLOEXEC (1U << 2)
#endif

int close_range(unsigned int first, unsigned int last, int flags) {
    pthread_once(&init_once, initialize);
    if (real_close_range == NULL) {
        errno = ENOSYS;
        return -1;
    }
    int result = real_close_range(first, last, flags);
    if (result == 0 && (flags & CLOSE_RANGE_CLOEXEC) == 0) {
        release_tracked_range(first, last);
    }
    return result;
}
