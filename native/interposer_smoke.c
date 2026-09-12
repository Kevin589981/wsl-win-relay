#define _GNU_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <linux/sched.h>
#include <netinet/in.h>
#include <sched.h>
#include <stdlib.h>
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

int main(void) {
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
    void *clone_stack = malloc(65536);
    if (clone_stack == NULL) {
        close(fd);
        return 18;
    }
    pid_t cloned = clone(clone_child, (char *)clone_stack + 65536, SIGCHLD, &fd);
    if (cloned < 0 || waitpid(cloned, NULL, 0) != cloned) {
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
        if (waitpid((pid_t)clone3_result, NULL, 0) != clone3_result) {
            close(fd);
            return 20;
        }
    } else if (errno != ENOSYS && errno != EPERM && errno != EINVAL) {
        close(fd);
        return 21;
    }
#endif
    pid_t child = fork();
    if (child < 0) {
        close_range((unsigned int)fd, (unsigned int)fd, 0);
        return 3;
    }
    if (child > 0) {
        close_range((unsigned int)fd, (unsigned int)fd, 0);
        return waitpid(child, NULL, 0) == child ? 0 : 4;
    }
    usleep(100000);
    close_range((unsigned int)fd, (unsigned int)fd, 0);
    _exit(0);
}
