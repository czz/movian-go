#ifndef ML_JNI_DISPATCH_H
#define ML_JNI_DISPATCH_H
#include <jni.h>

// JNI call dispatcher: all Java-side calls on the VideoRenderer must
// run on one stable native thread — upstream delivers frames from a
// single pthread (the VO thread) and VideoRenderer's ReentrantLock
// requires the same thread to lock()/unlock().  Go goroutines migrate
// across OS threads and cgo calls execute on runtime-managed stacks,
// which also breaks ART's stack bookkeeping (spurious
// StackOverflowError).  The dispatcher is a real pthread: it attaches
// in its entry point (ART records the genuine stack range) and runs
// every queued job on its own stack.
int   ml_jd_start(void *vm);
void *ml_jd_call(void *(*fn)(JNIEnv *, void *), void *arg);

#endif
