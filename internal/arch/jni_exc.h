#ifndef ML_AEXC_H
#define ML_AEXC_H
#include <jni.h>

// Deferred JNI exception reporting: ExceptionDescribe executes
// interpreted Java (Throwable.printStackTrace) which faults when the
// calling thread's stack is nearly exhausted — e.g. when the pending
// exception is itself StackOverflowError.  ml_aexc_report steals the
// throwable as a global ref (pure native op, needs no Java execution);
// a dedicated attached thread pops it via ml_aexc_take and describes
// it with ml_aexc_describe on a clean stack.
//
// ml_aexc_setctx records (in TLS) which Java method is about to be
// invoked so a reported exception can be attributed to a call site.
void        ml_aexc_setctx(const char *ctx);
void        ml_aexc_report(JNIEnv *e);
jthrowable  ml_aexc_take(const char **ctx_out);
void        ml_aexc_describe(JNIEnv *e, jthrowable t, const char *ctx);

#endif
