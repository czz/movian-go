#ifndef _STUBINC_FCNTL_H
#define _STUBINC_FCNTL_H
/* WASI fcntl.h has struct flock but lacks the lock commands; the
 * calls fail at runtime = intended no-lock path (single-process
 * wasm module). */
#include_next <fcntl.h>
#ifndef F_SETLK
#define F_RDLCK 0
#define F_WRLCK 1
#define F_UNLCK 2
#define F_SETLK 3
#define F_SETLKW 4
#endif
#endif
