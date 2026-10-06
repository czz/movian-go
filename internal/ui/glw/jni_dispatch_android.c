#include <pthread.h>
#include <android/log.h>
#include "jni_dispatch.h"

typedef struct ml_jd_job {
  void             *(*fn)(JNIEnv *, void *);
  void              *arg;
  void              *ret;
  int                done;
  struct ml_jd_job  *next;
} ml_jd_job;

static pthread_mutex_t  ml_jd_mu   = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t   ml_jd_cv   = PTHREAD_COND_INITIALIZER;
static ml_jd_job       *ml_jd_head;
static ml_jd_job     **ml_jd_tail = &ml_jd_head;
static JavaVM          *ml_jd_vm;
static int              ml_jd_state; // 0 = starting, 1 = ready, -1 = failed

static void *
ml_jd_main(void *arg)
{
  JNIEnv *env = NULL;
  pthread_mutex_lock(&ml_jd_mu);
  if((*ml_jd_vm)->AttachCurrentThread(ml_jd_vm, &env, NULL) != JNI_OK)
    env = NULL;
  ml_jd_state = env != NULL ? 1 : -1;
  if(env == NULL)
    __android_log_print(ANDROID_LOG_ERROR, "Movian",
                        "JNI dispatcher: attach failed");
  pthread_cond_broadcast(&ml_jd_cv);
  for(;;) {
    ml_jd_job *j;
    while(ml_jd_head == NULL)
      pthread_cond_wait(&ml_jd_cv, &ml_jd_mu);
    j = ml_jd_head;
    ml_jd_head = j->next;
    if(ml_jd_head == NULL)
      ml_jd_tail = &ml_jd_head;
    pthread_mutex_unlock(&ml_jd_mu);
    if(env != NULL)
      j->ret = j->fn(env, j->arg);
    pthread_mutex_lock(&ml_jd_mu);
    j->done = 1;
    // Several callers may wait on their own jobs — broadcast so the
    // owner of the completed job is always among the woken.
    pthread_cond_broadcast(&ml_jd_cv);
  }
  return NULL;
}

int
ml_jd_start(void *vm)
{
  pthread_t tid;
  int r;
  pthread_mutex_lock(&ml_jd_mu);
  if(ml_jd_vm == NULL) {
    ml_jd_vm = (JavaVM *)vm;
    if(pthread_create(&tid, NULL, ml_jd_main, NULL) != 0)
      ml_jd_state = -1;
  }
  while(ml_jd_state == 0 && ml_jd_vm != NULL)
    pthread_cond_wait(&ml_jd_cv, &ml_jd_mu);
  r = ml_jd_state;
  pthread_mutex_unlock(&ml_jd_mu);
  return r == 1 ? 0 : -1;
}

void *
ml_jd_call(void *(*fn)(JNIEnv *, void *), void *arg)
{
  ml_jd_job j;
  j.fn = fn;
  j.arg = arg;
  j.ret = NULL;
  j.done = 0;
  j.next = NULL;
  pthread_mutex_lock(&ml_jd_mu);
  *ml_jd_tail = &j;
  ml_jd_tail = &j.next;
  // Wake the dispatcher — it sleeps in pthread_cond_wait whenever the
  // queue is empty; without this the enqueued job is never seen.
  pthread_cond_broadcast(&ml_jd_cv);
  while(!j.done)
    pthread_cond_wait(&ml_jd_cv, &ml_jd_mu);
  pthread_mutex_unlock(&ml_jd_mu);
  return j.ret;
}
