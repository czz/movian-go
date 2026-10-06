#if !defined(_WIN32) && !defined(__wasi__)
#include "main.h"
#include <stdio.h>

/* TRACE() sink — routes the bundled libs' log output to Go via goDvdTrace */
extern void goDvdTrace(int level, char *subsys, char *msg);

void dvdlib_trace(int level, const char *subsys, const char *fmt, ...)
{
	char buf[1024];
	va_list ap;
	va_start(ap, fmt);
	vsnprintf(buf, sizeof(buf), fmt, ap);
	va_end(ap);
	goDvdTrace(level, (char *)subsys, buf);
}

#endif /* !_WIN32 && !__wasi__ — under wasi dvglue.c provides it */
