// Package task — C-canonical 1:1 port of src/task.c.
// Thread pool with task groups and fairness between groups.
// C: src/task.c (223 lines).
package task

import (
	"sync"
	"sync/atomic"
)

// MaxTaskThreads matches C: MAX_TASK_THREADS (task.c:28).
const MaxTaskThreads = 16

// MaxIdleTaskThreads matches C: MAX_IDLE_TASK_THREADS (task.c:29).
const MaxIdleTaskThreads = 2

// TaskFn matches C: task_fn_t (task.h:22).
// C: typedef void (task_fn_t)(void *opaque);
type TaskFn func(opaque any)

// task matches C: struct task (task.c:41-46).
type task struct {
	tFn     TaskFn
	tOpaque any
	tGroup  *TaskGroup
}

// TaskGroup matches C: struct task_group (task.c:34-38).
type TaskGroup struct {
	tgRefcount atomic.Int32
	tgTasks    []*task // C: struct task_queue tg_tasks (TAILQ)
}

// TaskSystem holds the state, matching C static variables
// (task.c:49-54):
//
//	static struct task_queue tasks = TAILQ_HEAD_INITIALIZER(tasks);
//	static struct task_group_queue task_groups = TAILQ_HEAD_INITIALIZER(task_groups);
//	static unsigned int num_task_threads;
//	static unsigned int num_task_threads_avail;
//	static hts_mutex_t task_mutex;
//	static hts_cond_t task_cond;
type TaskSystem struct {
	tasks               []*task
	taskGroups          []*TaskGroup
	numTaskThreads      int
	numTaskThreadsAvail int
	taskMutex           sync.Mutex
	taskCond            *sync.Cond
}

// NewTaskSystem matches C's INITIALIZER(taskinit) — initializes the
// task_mutex/task_cond statics. Callers inject the returned instance.
func NewTaskSystem() *TaskSystem {
	ts := &TaskSystem{}
	ts.taskCond = sync.NewCond(&ts.taskMutex)
	return ts
}

// taskGroupRelease matches C: task_group_release (task.c:60-67).
func taskGroupRelease(tg *TaskGroup) {
	if tg.tgRefcount.Add(-1) != 0 {
		return
	}
	// C: assert(TAILQ_FIRST(&tg->tg_tasks) == NULL)
	if len(tg.tgTasks) != 0 {
		panic("task_group_release: tg_tasks not empty")
	}
	// C: free(tg) — Go GC handles this
}

// taskThread matches C: task_thread (task.c:73-134).
func (ts *TaskSystem) taskThread() {
	ts.taskMutex.Lock()
	for {
		// C: t = TAILQ_FIRST(&tasks); tg = TAILQ_FIRST(&task_groups)
		var t *task
		var tg *TaskGroup
		if len(ts.tasks) > 0 {
			t = ts.tasks[0]
		}
		if len(ts.taskGroups) > 0 {
			tg = ts.taskGroups[0]
		}

		// C: if(t == NULL && tg == NULL) { ... }
		if t == nil && tg == nil {
			if ts.numTaskThreadsAvail == MaxIdleTaskThreads {
				break
			}
			ts.numTaskThreadsAvail++
			ts.taskCond.Wait()
			ts.numTaskThreadsAvail--
			continue
		}

		// C: if(t != NULL) { TAILQ_REMOVE(...); unlock; t_fn; free; lock; tg = TAILQ_FIRST }
		if t != nil {
			ts.tasks = ts.tasks[1:]
			ts.taskMutex.Unlock()
			t.tFn(t.tOpaque)
			ts.taskMutex.Lock()
			// C: tg = TAILQ_FIRST(&task_groups) — recheck after relock
			tg = nil
			if len(ts.taskGroups) > 0 {
				tg = ts.taskGroups[0]
			}
		}

		// C: if(tg != NULL) { ... }
		if tg != nil {
			ts.taskGroups = ts.taskGroups[1:]
			t = tg.tgTasks[0]
			ts.taskMutex.Unlock()
			t.tFn(t.tOpaque)
			ts.taskMutex.Lock()
			tg.tgTasks = tg.tgTasks[1:]
			// C: if(TAILQ_FIRST(&tg->tg_tasks) != NULL) TAILQ_INSERT_TAIL(...)
			if len(tg.tgTasks) > 0 {
				ts.taskGroups = append(ts.taskGroups, tg)
			}
			taskGroupRelease(tg)
		}
	}
	ts.numTaskThreads--
	ts.taskMutex.Unlock()
}

// taskSchedule matches C: task_schedule (task.c:140-152).
func (ts *TaskSystem) taskSchedule() {
	if ts.numTaskThreadsAvail > 0 {
		ts.taskCond.Signal()
	} else if ts.numTaskThreads < MaxTaskThreads {
		ts.numTaskThreads++
		go ts.taskThread()
	}
}

// Run matches C: task_run (task.c:158-168).
func (ts *TaskSystem) Run(fn TaskFn, opaque any) {
	ts.taskMutex.Lock()
	ts.tasks = append(ts.tasks, &task{tFn: fn, tOpaque: opaque})
	ts.taskSchedule()
	ts.taskMutex.Unlock()
}

// GroupCreate matches C: task_group_create (task.c:175-182).
func (ts *TaskSystem) GroupCreate() *TaskGroup {
	tg := &TaskGroup{}
	tg.tgRefcount.Store(1)
	return tg
}

// GroupDestroy matches C: task_group_destroy (task.c:188-192).
func (ts *TaskSystem) GroupDestroy(tg *TaskGroup) {
	taskGroupRelease(tg)
}

// RunInGroup matches C: task_run_in_group (task.c:198-213).
func (ts *TaskSystem) RunInGroup(fn TaskFn, opaque any, tg *TaskGroup) {
	tg.tgRefcount.Add(1)
	ts.taskMutex.Lock()
	if len(tg.tgTasks) == 0 {
		ts.taskGroups = append(ts.taskGroups, tg)
	}
	tg.tgTasks = append(tg.tgTasks, &task{tFn: fn, tOpaque: opaque, tGroup: tg})
	ts.taskSchedule()
	ts.taskMutex.Unlock()
}
