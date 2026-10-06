#!/usr/bin/env python3
"""Copy modernc.org/libc out of GOMODCACHE and patch syscall_musl.go so
that syscalls which the Android <= 8.x app seccomp filter kills with
SIGSYS (instead of returning ENOSYS) are never issued on 32-bit ARM.

musl's generated Go code wraps every such syscall in an ENOSYS
fallback to the time32/legacy variant — the same calls the upstream C
code relied on — so intercepting the X__syscallN / ___syscall_cp /
sleepSyscall funnel is enough to make the whole package behave as if
the kernel simply lacks them.

usage: seccomp32_libc.py <modcache libc dir> <destination dir>
"""

import os
import re
import shutil
import sys

GUARD = "\tif seccompBlockedArm(n) {\n\t\treturn -long(unix.ENOSYS)\n\t}\n"

HELPER = """
// seccompBlockedArm reports syscall numbers that kill (SIGSYS) under
// the app seccomp filter of Android <= 8.x on 32-bit ARM instead of
// returning ENOSYS. 397 is statx; everything >= 398 (rseq,
// io_pgetevents, the *_time64 family, io_uring, openat2, ...) postdates
// the filter's allowlist as well. musl has ENOSYS fallbacks to the
// legacy/time32 variants for every one it can issue.
func seccompBlockedArm(n long) bool {
	if runtime.GOARCH != "arm" {
		return false
	}
	return n >= 397
}
"""

# syscall_musl.go functions that funnel into unix.Syscall*.
FUNCS = [
    "___syscall_cp",
    "sleepSyscall",
    "X__syscall0",
    "X__syscall1",
    "X__syscall2",
    "X__syscall3",
    "X__syscall4",
    "X__syscall5",
    "X__syscall6",
]


def main():
    src, dst = sys.argv[1], sys.argv[2]
    marker = dst + ".patched"
    if os.path.exists(marker):
        return

    if os.path.isdir(dst):
        for d, _, _ in os.walk(dst):
            os.chmod(d, 0o755)
        shutil.rmtree(dst)
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    shutil.copytree(src, dst, symlinks=True)

    target = os.path.join(dst, "syscall_musl.go")
    os.chmod(target, 0o644)
    s = open(target).read()

    for fname in FUNCS:
        pat = re.compile(r"(func %s\(tls \*TLS[^{]*\{)\n" % fname)
        s, n = pat.subn(r"\1\n" + GUARD, s, count=1)
        if n != 1:
            sys.exit("cannot inject guard into %s" % fname)

    open(target, "w").write(s + HELPER)
    open(marker, "w").write("ok\n")


if __name__ == "__main__":
    main()
