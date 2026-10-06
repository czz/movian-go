/* Shim replacing movian's src/main.h for the bundled ext/dvd sources.
 * The patched libdvdread/libdvdnav use hts_mutex_* and TRACE() from main.h;
 * everything else they need is self-contained. */
#ifndef DVDLIB_MAIN_SHIM_H
#define DVDLIB_MAIN_SHIM_H

#include "arch/threads.h"
#include <stdio.h>
#include <stdarg.h>

/* movian trace levels (trace.h) */
#define TRACE_ERROR 0
#define TRACE_INFO  1
#define TRACE_DEBUG 2

void dvdlib_trace(int level, const char *subsys, const char *fmt, ...);
#define TRACE(level, subsys, ...) dvdlib_trace(level, subsys, __VA_ARGS__)

#endif
