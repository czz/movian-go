/* Shim for movian's src/arch/threads.h (posix variant) — the bundled
 * ext/dvd sources only need hts_mutex_* from here. Deliberately does
 * NOT define TRACE: in movian that lives in main.h only, and files
 * that never include main.h must keep their #ifdef TRACE debug code
 * compiled out. */
#ifndef DVDLIB_THREADS_SHIM_H
#define DVDLIB_THREADS_SHIM_H

#include <pthread.h>

typedef pthread_mutex_t hts_mutex_t;
#define hts_mutex_init(m)    pthread_mutex_init(m, NULL)
#define hts_mutex_lock(m)    pthread_mutex_lock(m)
#define hts_mutex_unlock(m)  pthread_mutex_unlock(m)
#define hts_mutex_destroy(m) pthread_mutex_destroy(m)

#endif
