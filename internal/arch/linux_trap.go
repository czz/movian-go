//go:build linux && !android

package arch

// Canonical port of src/arch/linux/linux_trap.c — the SIGSEGV-family
// trap handler (the __linux__ branch; the #else no-op branch is N/A).
//
// The entire handler machinery lives in the cgo preamble because it
// runs in signal context (backtrace/dladdr/fork+addr2line). TRAPMSG
// maps to fprintf(stderr) — C routes through TRACE(TRACE_EMERG) which
// reaches the same fd; going through the Go trace package from a
// signal handler would add no fidelity (and C's trace lock is no
// safer in signal context).

/*
#cgo CFLAGS: -D_GNU_SOURCE
#cgo LDFLAGS: -ldl
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <stdarg.h>
#include <signal.h>
#include <fcntl.h>
#include <errno.h>
#include <ucontext.h>
#include <execinfo.h>
#include <sys/prctl.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <dlfcn.h>
#include <link.h>
#include <limits.h>

#define MAXFRAMES 100

extern char **environ;

// C: int (*extra_traphandler)(int sig, siginfo_t *si, void *UC)
// (linux_trap.c:45) — exported for cedar.c. The Go callback cannot be
// invoked from signal context (cgocallback cannot run there — doing so
// is itself a crash), so the Go side only registers a C-level flag:
// flag set → the handler was "handled" (C's return-0 → skip dump),
// unset → fall through to the dump. Set via linux_trap_extra_present.
static volatile int extra_trap_present;

static void linux_trap_extra_present(int v) {
	extra_trap_present = v;
}

static int call_extra_traphandler(int sig, siginfo_t *si, void *UC) {
	return extra_trap_present ? 0 : 1;
}

// Previous signal dispositions. Go's runtime installs its own handlers
// (for goroutine-dump on fault) before linux_trap_init runs; C had no
// prior handler to preserve, but replacing Go's would mask every
// runtime fault (nil derefs would die silently as bare SIGSEGV). We
// chain to the previous handler after our dump.
static struct sigaction prev_sa[32];

static char line1[200];
static char libs[2048];
static char self[PATH_MAX];
static char *symbuf;

// C: load_symfile (linux_trap.c:61-84) — mmaps <binary>.syms next to
// the executable for offline symbol resolution.
static void load_symfile(const char *binary) {
	char buf[1024];
	snprintf(buf, sizeof(buf), "%s.syms", binary);

	int fd = open(buf, O_RDONLY);
	if(fd == -1)
		return;

	struct stat st;
	fstat(fd, &st);

	symbuf = mmap(NULL, st.st_size + 4096, PROT_READ | PROT_WRITE,
		      MAP_PRIVATE, fd, 0);
	if(symbuf == MAP_FAILED) {
		fprintf(stderr, "Unable to map symfile %s -- %s\n",
			buf, strerror(errno));
		close(fd);
		symbuf = NULL;
		return;
	}
}

// C: resolve_syms (linux_trap.c:89-121) — resolves frames against the
// .syms file (sorted "addr name" lines).
static int resolve_syms(void **ptr, const char **symvec, int *symoffset, int frames) {
	char *s = symbuf;
	int i;
	for(i = 0; i < frames; i++) {
		symvec[i] = NULL;
		symoffset[i] = 0;
	}

	if(s == NULL)
		return -1;

	while(s) {
		int64_t addr = strtol(s, NULL, 16);
		if(addr > 0x10000) {
			for(i = 0; i < frames; i++) {
				int64_t a0 = (intptr_t)ptr[i];
				if(a0 >= addr) {
					symvec[i] = strchr(s, ' ');
					if(symvec[i])
						symvec[i]++;
					symoffset[i] = a0 - addr;
				}
			}
		}

		s = strchr(s, '\n');
		if(s == NULL)
			return 0;
		*s++ = 0;
	}
	return 0;
}

// C: sappend (linux_trap.c:125-133)
static void sappend(char *buf, size_t l, const char *fmt, ...) {
	va_list ap;
	va_start(ap, fmt);
	vsnprintf(buf + strlen(buf), l - strlen(buf), fmt, ap);
	va_end(ap);
}

// C: add2lineresolve (linux_trap.c:139-203) — forks
// /usr/bin/addr2line -e <binary> <addr-1> and reads one line.
static int add2lineresolve(const char *binary, void *addr, char *buf0, size_t buflen) {
	char *buf = buf0;
	int fd[2], r, f;
	const char *argv[5];
	pid_t p;
	char addrstr[30], *cp;

	if(access("/usr/bin/addr2line", X_OK))
		return -1;

	argv[0] = "addr2line";
	argv[1] = "-e";
	argv[2] = binary;
	argv[3] = addrstr;
	argv[4] = NULL;

	snprintf(addrstr, sizeof(addrstr), "%p", (void *)((intptr_t)addr-1));

	if(pipe(fd) == -1)
		return -1;

	if((p = fork()) == -1)
		return -1;

	if(p == 0) {
		close(0);
		close(2);
		close(fd[0]);
		dup2(fd[1], 1);
		close(fd[1]);
		if((f = open("/dev/null", O_RDWR)) == -1)
			_exit(1);

		dup2(f, 0);
		dup2(f, 2);
		close(f);

		execve("/usr/bin/addr2line", (char *const *) argv, environ);
		_exit(0);
	}

	close(fd[1]);
	*buf = 0;
	while(buflen > 1) {
		r = read(fd[0], buf, buflen);
		if(r < 1)
			break;

		buf += r;
		buflen -= r;
		*buf = 0;
		cp = strchr(buf0, '\n');
		if(cp != NULL) {
			*cp = 0;
			break;
		}
	}
	close(fd[0]);
	return 0;
}

// C: addr2text (linux_trap.c:206-233) — dladdr symbol+offset, then
// addr2line file:line, then fname, then raw pointer.
static void addr2text(char *out, size_t outlen, void *ptr) {
	Dl_info dli = {};
	char buf[256];
	int r = dladdr(ptr, &dli);

	if(r && dli.dli_sname != NULL && dli.dli_saddr != NULL) {
		snprintf(out, outlen, "%s+0x%tx  (%s)",
			 dli.dli_sname, (char*)ptr - (char*)dli.dli_saddr, dli.dli_fname);
		return;
	}

	if(self[0] && !add2lineresolve(self, ptr, buf, sizeof(buf))) {
		snprintf(out, outlen, "%s %p", buf, ptr);
		return;
	}

	if(dli.dli_fname != NULL && dli.dli_fbase != NULL) {
		snprintf(out, outlen, "%s %p", dli.dli_fname, ptr);
		return;
	}
	snprintf(out, outlen, "%p", ptr);
}

// C: dumpstack (linux_trap.c:235-257) — TRAPMSG per frame using the
// .syms file when possible, addr2text otherwise.
static void dumpstack(void *frames[], int nframes) {
	const char *sym[MAXFRAMES];
	int symoffset[MAXFRAMES];
	char buf[256];
	int i;

	fprintf(stderr, "STACKTRACE (%d frames)\n", nframes);

	if(nframes > MAXFRAMES)
		nframes = MAXFRAMES;
	resolve_syms(frames, sym, symoffset, nframes);

	for(i = 0; i < nframes; i++) {
		if(sym[i] == NULL) {
			addr2text(buf, sizeof(buf), frames[i]);
		} else {
			snprintf(buf, sizeof(buf), "%s+0x%x", sym[i], symoffset[i]);
		}
		fprintf(stderr, "%s\n", buf);
	}
}

// C: traphandler (linux_trap.c:262-342)
static void traphandler(int sig, siginfo_t *si, void *UC) {
	ucontext_t *uc = UC;

	if(call_extra_traphandler(sig, si, UC) == 0)
		return;

	static void *frames[MAXFRAMES];
	char buf[256];
	int nframes = backtrace(frames, MAXFRAMES);
	const char *reason = NULL;

	char prname[17] = {0};

	prctl(PR_GET_NAME, prname, 0, 0, 0);

	fprintf(stderr, "Signal: %d in thread %s - %s\n", sig, prname, line1);

	switch(sig) {
	case SIGSEGV:
		switch(si->si_code) {
		case SEGV_MAPERR:  reason = "Address not mapped"; break;
		case SEGV_ACCERR:  reason = "Access error"; break;
		}
		break;
	case SIGFPE:
		switch(si->si_code) {
		case FPE_INTDIV:  reason = "Integer division by zero"; break;
		}
		break;
	}

	addr2text(buf, sizeof(buf), si->si_addr);

	fprintf(stderr, "Fault address %s (%s)\n", buf, reason ? reason : "N/A");
	fprintf(stderr, "Loaded libraries: %s\n", libs);

#if defined(__arm__)
	fprintf(stderr, "   trap_no = 0x%08lx\n", uc->uc_mcontext.trap_no);
	fprintf(stderr, "error_code = 0x%08lx\n", uc->uc_mcontext.error_code);
	fprintf(stderr, "   oldmask = 0x%08lx\n", uc->uc_mcontext.oldmask);
	fprintf(stderr, "        R0 = 0x%08lx\n", uc->uc_mcontext.arm_r0);
	fprintf(stderr, "        R1 = 0x%08lx\n", uc->uc_mcontext.arm_r1);
	fprintf(stderr, "        R2 = 0x%08lx\n", uc->uc_mcontext.arm_r2);
	fprintf(stderr, "        R3 = 0x%08lx\n", uc->uc_mcontext.arm_r3);
	fprintf(stderr, "        R4 = 0x%08lx\n", uc->uc_mcontext.arm_r4);
	fprintf(stderr, "        R5 = 0x%08lx\n", uc->uc_mcontext.arm_r5);
	fprintf(stderr, "        R6 = 0x%08lx\n", uc->uc_mcontext.arm_r6);
	fprintf(stderr, "        R7 = 0x%08lx\n", uc->uc_mcontext.arm_r7);
	fprintf(stderr, "        R8 = 0x%08lx\n", uc->uc_mcontext.arm_r8);
	fprintf(stderr, "        R9 = 0x%08lx\n", uc->uc_mcontext.arm_r9);
	fprintf(stderr, "       R10 = 0x%08lx\n", uc->uc_mcontext.arm_r10);
	fprintf(stderr, "        FP = 0x%08lx\n", uc->uc_mcontext.arm_fp);
	fprintf(stderr, "        IP = 0x%08lx\n", uc->uc_mcontext.arm_ip);
	fprintf(stderr, "        SP = 0x%08lx\n", uc->uc_mcontext.arm_sp);
	fprintf(stderr, "        LR = 0x%08lx\n", uc->uc_mcontext.arm_lr);
	fprintf(stderr, "        PC = 0x%08lx\n", uc->uc_mcontext.arm_pc);
	fprintf(stderr, "      CPSR = 0x%08lx\n", uc->uc_mcontext.arm_cpsr);
	fprintf(stderr, "fault_addr = 0x%08lx\n", uc->uc_mcontext.fault_address);
#else
	char tmpbuf[1024];
	snprintf(tmpbuf, sizeof(tmpbuf), "Register dump [%d]: ", NGREG);
	int i;
	for(i = 0; i < NGREG; i++) {
#if __WORDSIZE == 64
		sappend(tmpbuf, sizeof(tmpbuf), "%016llx ", (unsigned long long)uc->uc_mcontext.gregs[i]);
#else
		sappend(tmpbuf, sizeof(tmpbuf), "%08x ", (unsigned int)uc->uc_mcontext.gregs[i]);
#endif
	}
	fprintf(stderr, "%s\n", tmpbuf);
#endif

	dumpstack(frames, nframes);

	// Chain to the previously-installed handler (Go runtime's):
	// it converts runtime-level faults into panics/goroutine dumps
	// instead of a bare signal death, or re-dies on real corruption.
	if(sig < 32) {
		struct sigaction *o = &prev_sa[sig];
		if(o->sa_flags & SA_SIGINFO) {
			if(o->sa_sigaction != NULL) {
				o->sa_sigaction(sig, si, UC);
			}
		} else if(o->sa_handler != NULL && o->sa_handler != SIG_DFL &&
			  o->sa_handler != SIG_IGN) {
			o->sa_handler(sig);
		}
	}
	_exit(8);
}

// C: callback (linux_trap.c:345-353) — dl_iterate_phdr collector.
static int callback(struct dl_phdr_info *info, size_t size, void *data) {
	if(info->dlpi_name[0])
		sappend(libs, sizeof(libs), "%s ", info->dlpi_name);
	return 0;
}

// C: linux_trap_init (linux_trap.c:356-396)
static void linux_trap_init_c(const char *appname, const char *appversion, const char *binary) {
	struct sigaction sa, old;
	char path[256];
	int r;

	r = readlink("/proc/self/exe", self, sizeof(self) - 1);
	if(r == -1)
		self[0] = 0;
	else
		self[r] = 0;

	snprintf(line1, sizeof(line1),
		 "PRG: %s (%s) EXE: %s, CWD: %s ", appname, appversion,
		 self, getcwd(path, sizeof(path)));

	dl_iterate_phdr(callback, NULL);

	memset(&sa, 0, sizeof(sa));

	sigset_t m;
	sigemptyset(&m);
	sigaddset(&m, SIGSEGV);
	sigaddset(&m, SIGBUS);
	sigaddset(&m, SIGILL);
	sigaddset(&m, SIGABRT);
	sigaddset(&m, SIGFPE);

	sa.sa_sigaction = traphandler;
	// SA_ONSTACK: Go registers a per-thread sigaltstack; running the
	// handler on it keeps the dump path alive when the fault is a
	// stack overflow (and is a no-op for C-created threads without
	// an altstack).
	sa.sa_flags = SA_SIGINFO | SA_ONSTACK;
	sigaction(SIGSEGV, &sa, &prev_sa[SIGSEGV]);
	sigaction(SIGBUS,  &sa, &prev_sa[SIGBUS]);
	sigaction(SIGILL,  &sa, &prev_sa[SIGILL]);
	sigaction(SIGABRT, &sa, &prev_sa[SIGABRT]);
	sigaction(SIGFPE,  &sa, &prev_sa[SIGFPE]);

	sigprocmask(SIG_UNBLOCK, &m, NULL);

	load_symfile(binary);
}

// C: stackdump (linux_trap.c:423-427) — backtrace + dumpstack.
static void stackdump_c(void) {
	static void *frames[MAXFRAMES];
	int nframes = backtrace(frames, MAXFRAMES);
	dumpstack(frames, nframes);
}

// C: panic (linux_trap.c:401-419) — resets handlers, dumps, exit(1).
static void panic_c(const char *msg) {
	static void *frames[MAXFRAMES];
	int nframes = backtrace(frames, MAXFRAMES);

	signal(SIGSEGV, SIG_DFL);
	signal(SIGBUS,  SIG_DFL);
	signal(SIGILL,  SIG_DFL);
	signal(SIGABRT, SIG_DFL);
	signal(SIGFPE,  SIG_DFL);

	fprintf(stderr, "PANIC: %s\n", msg);
	dumpstack(frames, nframes);
	exit(1);
}
*/
import "C"

import (
	"unsafe"
)

// ExtraTrapHandler — C: int (*extra_traphandler)(int sig, siginfo_t
// *si, void *UC) (linux_trap.c:45). Returning false lets the default
// crash dump run; returning true suppresses it (cedar.c semantics).
//
// NOTE: the C side cannot call into Go from signal context — setting
// this marks the extra handler present (C flag → dump suppressed) but
// the Go function itself is never invoked from traphandler.
var extraTrapHandler func(sig int, si, uc unsafe.Pointer) bool

//export goExtraTrapHandler
func goExtraTrapHandler(sig C.int, si, uc unsafe.Pointer) C.int {
	if extraTrapHandler == nil {
		return 1 // C: extra_traphandler == NULL → skip call
	}
	if !extraTrapHandler(int(sig), si, uc) {
		return 0 // C: return 0 → suppress default handler
	}
	return 1
}

// SetExtraTrapHandler — installs the extra trap handler and flips the C-side
// presence flag used by traphandler (signal-context safe).
func SetExtraTrapHandler(fn func(sig int, si, uc unsafe.Pointer) bool) {
	extraTrapHandler = fn
	if fn != nil {
		C.linux_trap_extra_present(1)
	} else {
		C.linux_trap_extra_present(0)
	}
}

// LinuxTrapStart — C: linux_trap_init (linux_trap.c:356-396).
// appversion and binary feed the crash banner and the .syms lookup.
func LinuxTrapStart(appversion, binary string) error {
	an := C.CString("movian-go")
	av := C.CString(appversion)
	bn := C.CString(binary)
	C.linux_trap_init_c(an, av, bn)
	C.free(unsafe.Pointer(an))
	C.free(unsafe.Pointer(av))
	C.free(unsafe.Pointer(bn))
	return nil
}

// Panic — C: panic() (linux_trap.c:401-419). Reset handlers, dump the
// backtrace and exit(1) — unlike C's va_list version Go takes a
// pre-formatted message (callers format with Sprintf).
func Panic(msg string) {
	m := C.CString(msg)
	C.panic_c(m) // noreturn
}

// StackDump — C: stackdump(fac) (linux_trap.c:423-427). Dumps the
// C-side backtrace through the canonical dumpstack (.syms/addr2line
// resolution), like C. fac is the trace facility (unused in C's
// implementation beyond the message prefix).
func StackDump(facility string) {
	C.stackdump_c()
}
