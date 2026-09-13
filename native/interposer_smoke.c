#define _GNU_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <linux/sched.h>
#include <netinet/in.h>
#include <pthread.h>
#include <sched.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

#ifndef CLOSE_RANGE_CLOEXEC
#define CLOSE_RANGE_CLOEXEC (1U << 2)
#endif

static int clone_child(void *argument) {
    int fd = *(int *)argument;
    return close_range((unsigned int)fd, (unsigned int)fd, 0) == 0 ? 0 : 1;
}

static void *thread_child(void *argument) {
    int *result = (int *)argument;
    struct sockaddr_in address = {0};
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);

    int tcp = socket(AF_INET, SOCK_STREAM, 0);
    if (tcp < 0) {
        *result = 1;
        return NULL;
    }
    address.sin_port = htons(47128);
    if (bind(tcp, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(tcp, 16) < 0) {
        close(tcp);
        *result = 2;
        return NULL;
    }
    close(tcp);

    int udp = socket(AF_INET, SOCK_DGRAM, 0);
    if (udp < 0) {
        *result = 3;
        return NULL;
    }
    address.sin_port = htons(47129);
    if (bind(udp, (struct sockaddr *)&address, sizeof(address)) < 0) {
        close(udp);
        *result = 4;
        return NULL;
    }
    close(udp);
    *result = 0;
    return NULL;
}

static int wait_for_child(pid_t child) {
    int status;
    pid_t result;
    do {
        result = waitpid(child, &status, 0);
    } while (result < 0 && errno == EINTR);
    return result == child ? 0 : -1;
}

static int adoption_failure_smoke(void) {
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) return 1;
    struct sockaddr_in address = {0};
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    address.sin_port = htons(47131);
    if (bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 16) < 0) {
        close(fd);
        return 2;
    }
    errno = 0;
    pid_t child = fork();
    if (child >= 0) {
        close(fd);
        return 3;
    }
    int saved = errno;
    close(fd);
    /* The control test server returns errno 5 for a rejected ADOPT. */
    return saved == EIO ? 0 : 4;
}

static int clone_noop(void *argument) {
    (void)argument;
    return 0;
}

static int clone_adoption_failure_smoke(void) {
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) return 1;
    struct sockaddr_in address = {0};
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    address.sin_port = htons(47132);
    if (bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 16) < 0) {
        close(fd);
        return 2;
    }
    void *stack = malloc(65536);
    if (stack == NULL) {
        close(fd);
        return 3;
    }
    errno = 0;
    pid_t child = clone(clone_noop, (char *)stack + 65536, SIGCHLD, NULL);
    int saved = errno;
    free(stack);
    close(fd);
    return child < 0 && saved == EIO ? 0 : 4;
}

int main(int argc, char **argv) {
    if (argc > 1 && strcmp(argv[1], "adopt-failure") == 0) {
        return adoption_failure_smoke();
    }
    if (argc > 1 && strcmp(argv[1], "adopt-failure-clone") == 0) {
        return clone_adoption_failure_smoke();
    }
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) return 1;
    struct sockaddr_in address = {0};
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    address.sin_port = htons(47123);
    if (bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 16) < 0) {
        close_range((unsigned int)fd, (unsigned int)fd, 0);
        return 2;
    }
    int raw = socket(AF_INET, SOCK_STREAM, 0);
    if (raw < 0) {
        close(fd);
        return 14;
    }
    address.sin_port = htons(47126);
    if (syscall(SYS_bind, raw, (struct sockaddr *)&address, sizeof(address)) < 0 ||
        syscall(SYS_listen, raw, 16) < 0) {
        close(raw);
        close(fd);
        return 15;
    }
    close(raw);
    int rejected = socket(AF_INET, SOCK_STREAM, 0);
    if (rejected < 0) {
        close(fd);
        return 12;
    }
    address.sin_port = htons(47125);
    if (bind(rejected, (struct sockaddr *)&address, sizeof(address)) < 0 ||
        syscall(SYS_listen, rejected, 16) == 0 || errno != EADDRINUSE) {
        close(rejected);
        close(fd);
        return 13;
    }
    close(rejected);
    address.sin_port = htons(47123);
    int duplicate = fcntl(fd, F_DUPFD, 0);
    if (duplicate < 0) {
        close(fd);
        return 5;
    }
    if (close_range((unsigned int)fd, (unsigned int)fd, 0) < 0) {
        close(fd);
        close(duplicate);
        return 6;
    }
    fd = duplicate;
    if (close_range((unsigned int)fd, (unsigned int)fd, CLOSE_RANGE_CLOEXEC) < 0) {
        close(fd);
        return 7;
    }
    int udp = socket(AF_INET, SOCK_DGRAM, 0);
    if (udp < 0) {
        close(fd);
        return 8;
    }
    address.sin_port = htons(47124);
    if (bind(udp, (struct sockaddr *)&address, sizeof(address)) < 0) {
        close(udp);
        close(fd);
        return 9;
    }
    close(udp);
    int ephemeral = socket(AF_INET, SOCK_DGRAM, 0);
    if (ephemeral < 0) {
        close(fd);
        return 10;
    }
    address.sin_port = 0;
    if (bind(ephemeral, (struct sockaddr *)&address, sizeof(address)) < 0) {
        close(ephemeral);
        close(fd);
        return 11;
    }
    close(ephemeral);
    int raw_udp = socket(AF_INET, SOCK_DGRAM, 0);
    if (raw_udp < 0) {
        close(fd);
        return 16;
    }
    address.sin_port = htons(47127);
    if (syscall(SYS_bind, raw_udp, (struct sockaddr *)&address, sizeof(address)) < 0) {
        close(raw_udp);
        close(fd);
        return 17;
    }
    close(raw_udp);
    int delayed = socket(AF_INET, SOCK_STREAM, 0);
    if (delayed < 0) {
        close(fd);
        return 23;
    }
    address.sin_port = htons(47130);
    if (bind(delayed, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(delayed, 16) < 0) {
        close(delayed);
        close(fd);
        return 24;
    }
    close(delayed);
    void *clone_stack = malloc(65536);
    if (clone_stack == NULL) {
        close(fd);
        return 18;
    }
    pid_t cloned = clone(clone_child, (char *)clone_stack + 65536, SIGCHLD, &fd);
    if (cloned < 0 || wait_for_child(cloned) < 0) {
        free(clone_stack);
        close(fd);
        return 19;
    }
    free(clone_stack);
#ifdef SYS_clone3
    struct clone_args clone3_arguments = {0};
    clone3_arguments.exit_signal = SIGCHLD;
    long clone3_result = syscall(SYS_clone3, &clone3_arguments, sizeof(clone3_arguments));
    if (clone3_result == 0) {
        (void)close_range((unsigned int)fd, (unsigned int)fd, 0);
        _exit(0);
    }
    if (clone3_result > 0) {
        if (wait_for_child((pid_t)clone3_result) < 0) {
            close(fd);
            return 20;
        }
    } else if (errno != ENOSYS && errno != EPERM && errno != EINVAL && errno != ENOTSUP) {
        close(fd);
        return 21;
    }
#endif
    int thread_result = -1;
    pthread_t thread;
    if (pthread_create(&thread, NULL, thread_child, &thread_result) != 0 ||
        pthread_join(thread, NULL) != 0 || thread_result != 0) {
        close_range((unsigned int)fd, (unsigned int)fd, 0);
        return 22;
    }
    pid_t child = fork();
    if (child < 0) {
        close_range((unsigned int)fd, (unsigned int)fd, 0);
        return 3;
    }
    if (child > 0) {
        close_range((unsigned int)fd, (unsigned int)fd, 0);
        if (wait_for_child(child) < 0) {
            return 4;
        }
        pid_t vforked = vfork();
        if (vforked < 0) {
            if (errno != ENOSYS && errno != EPERM && errno != EINVAL && errno != EAGAIN && errno != ENOTSUP) {
                return 25;
            }
        } else {
            if (vforked == 0) {
                _exit(0);
            }
            if (wait_for_child(vforked) < 0) {
                return 26;
            }
        }
        errno = 0;
        if (syscall(SYS_clone, (unsigned long)(CLONE_FILES | SIGCHLD), 0, NULL, NULL, NULL) != -1 ||
            errno != ENOTSUP) {
            return 26;
        }
#ifdef SYS_clone3
        struct clone_args shared_clone3 = {0};
        shared_clone3.flags = CLONE_FILES;
        shared_clone3.exit_signal = SIGCHLD;
        errno = 0;
        if (syscall(SYS_clone3, &shared_clone3, sizeof(shared_clone3)) != -1 || errno != ENOTSUP) {
            return 27;
        }
        errno = 0;
        if (syscall(SYS_clone3, (void *)1, sizeof(shared_clone3)) != -1 || errno != ENOTSUP) {
            return 28;
        }
#endif
        return 0;
    }
    usleep(100000);
    close_range((unsigned int)fd, (unsigned int)fd, 0);
    _exit(0);
}
