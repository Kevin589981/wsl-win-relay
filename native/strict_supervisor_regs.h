#ifndef WSL_WIN_RELAY_STRICT_SUPERVISOR_REGS_H
#define WSL_WIN_RELAY_STRICT_SUPERVISOR_REGS_H

#include <elf.h>
#include <stdint.h>
#include <sys/ptrace.h>
#include <sys/uio.h>
#include <sys/types.h>

#if defined(__x86_64__)

#include <sys/user.h>
typedef struct user_regs_struct wwr_regs;

static int wwr_get_regs(pid_t pid, wwr_regs *regs) {
    return ptrace(PTRACE_GETREGS, pid, 0, regs);
}

static int wwr_set_regs(pid_t pid, wwr_regs *regs) {
    return ptrace(PTRACE_SETREGS, pid, 0, regs);
}

#define WWR_SYSCALL(regs) ((regs)->orig_rax)
#define WWR_RETURN(regs) ((regs)->rax)
#define WWR_ARG(regs, index) ((index) == 0 ? (regs)->rdi : \
                             (index) == 1 ? (regs)->rsi : \
                             (index) == 2 ? (regs)->rdx : \
                             (index) == 3 ? (regs)->r10 : \
                             (index) == 4 ? (regs)->r8 : (regs)->r9)
#define WWR_ARCH_NAME "amd64"

#elif defined(__aarch64__)

struct wwr_regs {
    uint64_t regs[31];
    uint64_t sp;
    uint64_t pc;
    uint64_t pstate;
};
typedef struct wwr_regs wwr_regs;

static int wwr_get_regs(pid_t pid, wwr_regs *regs) {
    struct iovec iov = {.iov_base = regs, .iov_len = sizeof(*regs)};
    return ptrace(PTRACE_GETREGSET, pid, (void *)(uintptr_t)NT_PRSTATUS, &iov);
}

static int wwr_set_regs(pid_t pid, wwr_regs *regs) {
    struct iovec iov = {.iov_base = regs, .iov_len = sizeof(*regs)};
    return ptrace(PTRACE_SETREGSET, pid, (void *)(uintptr_t)NT_PRSTATUS, &iov);
}

#define WWR_SYSCALL(regs) ((regs)->regs[8])
#define WWR_RETURN(regs) ((regs)->regs[0])
#define WWR_ARG(regs, index) ((regs)->regs[(index)])
#define WWR_ARCH_NAME "aarch64"

#else
#error "wsl-win-relay-strict requires an implemented ptrace register adapter"
#endif

#endif
