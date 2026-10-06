#ifndef ML_JNI_EXC_H
#define ML_JNI_EXC_H
#include <jni.h>

// Deferred JNI exception reporting: ExceptionDescribe executes
// interpreted Java (Throwable.printStackTrace) which faults when the
// calling thread's stack is nearly exhausted — e.g. when the pending
// exception is itself StackOverflowError.  ml_exc_report steals the
// throwable as a global ref (pure native op, needs no Java execution);
// a dedicated attached thread pops it via ml_exc_take and describes
// it with ml_exc_describe on a clean stack.
//
// ml_exc_setctx records (in TLS) which Java method is about to be
// invoked so a reported exception can be attributed to a call site.
void        ml_exc_setctx(const char *ctx);
void        ml_exc_report(JNIEnv *e);
jthrowable  ml_exc_take(const char **ctx_out);
void        ml_exc_describe(JNIEnv *e, jthrowable t, const char *ctx);

#endif
