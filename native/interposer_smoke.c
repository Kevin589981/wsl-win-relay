#define _GNU_SOURCE
#include <arpa/inet.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

#ifndef CLOSE_RANGE_CLOEXEC
#define CLOSE_RANGE_CLOEXEC (1U << 2)
#endif

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
