#include <stdlib.h>
#include <pthread.h>
#include <unistd.h>
#include <android/log.h>
#include "jni_exc.h"

typedef struct ml_aexc_node {
  jthrowable t;
  const char *ctx;
  struct ml_aexc_node *next;
} ml_aexc_node;

static ml_aexc_node      *ml_aexc_head;
static pthread_mutex_t   ml_aexc_mu = PTHREAD_MUTEX_INITIALIZER;
static __thread const char *ml_aexc_ctx;

void
ml_aexc_setctx(const char *ctx)
{
  ml_aexc_ctx = ctx;
}

void
ml_aexc_report(JNIEnv *e)
{
  ml_aexc_node *n;
  jthrowable t;
  pthread_attr_t attr;
  void *saddr = NULL;
  size_t ssize = 0;
  char sp;
  if(!(*e)->ExceptionCheck(e))
    return;
  if(pthread_getattr_np(pthread_self(), &attr) == 0) {
    pthread_attr_getstack(&attr, &saddr, &ssize);
    pthread_attr_destroy(&attr);
  }
  __android_log_print(ANDROID_LOG_ERROR, "Movian",
                      "JNI exc during %s on tid %d sp=%p stk=[%p..%p]",
                      ml_aexc_ctx ? ml_aexc_ctx : "?", (int)gettid(), &sp,
                      saddr, (char *)saddr + ssize);
  t = (*e)->ExceptionOccurred(e);
  if(t == NULL) {
    (*e)->ExceptionClear(e);
    return;
  }
  (*e)->ExceptionClear(e);
  n = malloc(sizeof(ml_aexc_node));
  if(n == NULL)
    return;
  n->t = (jthrowable)(*e)->NewGlobalRef(e, t);
  (*e)->DeleteLocalRef(e, t);
  n->ctx = ml_aexc_ctx;
  pthread_mutex_lock(&ml_aexc_mu);
  n->next = ml_aexc_head;
  ml_aexc_head = n;
  pthread_mutex_unlock(&ml_aexc_mu);
}

jthrowable
ml_aexc_take(const char **ctx_out)
{
  ml_aexc_node *n;
  jthrowable t;
  pthread_mutex_lock(&ml_aexc_mu);
  n = ml_aexc_head;
  if(n != NULL)
    ml_aexc_head = n->next;
  pthread_mutex_unlock(&ml_aexc_mu);
  if(n == NULL)
    return NULL;
  t = n->t;
  *ctx_out = n->ctx;
  free(n);
  return t;
}

void
ml_aexc_describe(JNIEnv *e, jthrowable t, const char *ctx)
{
  __android_log_print(ANDROID_LOG_ERROR, "Movian",
                      "JNI exception during %s", ctx ? ctx : "?");
  (*e)->Throw(e, t);
  (*e)->ExceptionDescribe(e);
  (*e)->ExceptionClear(e);
  (*e)->DeleteGlobalRef(e, t);
}
