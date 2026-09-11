#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

int main(void) {
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) return 1;
    struct sockaddr_in address = {0};
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    address.sin_port = htons(47123);
    if (bind(fd, (struct sockaddr *)&address, sizeof(address)) < 0 || listen(fd, 16) < 0) {
        close(fd);
        return 2;
    }
    pid_t child = fork();
    if (child < 0) {
        close(fd);
        return 3;
    }
    if (child > 0) {
        close(fd);
        return waitpid(child, NULL, 0) == child ? 0 : 4;
    }
    usleep(100000);
    close(fd);
    _exit(0);
}
