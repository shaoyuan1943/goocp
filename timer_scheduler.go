package goocp

import (
	"container/heap"
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/shaoyuan1943/gokcp"
)

type Task struct {
	exec func()
	ts   uint32
}

type TaskList []*Task

func (t TaskList) Len() int {
	return len(t)
}

func (t TaskList) Less(i int, j int) bool {
	return t[i].ts < t[j].ts
}

func (t TaskList) Swap(i int, j int) {
	t[i], t[j] = t[j], t[i]
}

func (t *TaskList) Push(v interface{}) {
	*t = append(*t, v.(*Task))
}

func (t *TaskList) Pop() interface{} {
	o := *t
	n := len(o)
	v := o[n-1]
	*t = o[:n-1]
	return v
}

type TimerScheduler struct {
	pendingTasks TaskList
	taskC        chan *Task
	taskInC      chan struct{}
	mx           sync.Mutex
	ctx          context.Context
	quitF        context.CancelFunc
}

func NewTimerScheduler() *TimerScheduler {
	ts := &TimerScheduler{
		taskC:   make(chan *Task),
		taskInC: make(chan struct{}),
	}

	ts.ctx, ts.quitF = context.WithCancel(context.Background())
	n := runtime.GOMAXPROCS(0)
	for i := 0; i < n; i++ {
		go ts.scheduled()
	}

	go ts.pending()
	return ts
}

func (ts *TimerScheduler) scheduled() {
	var tasks TaskList
	updateTimer := time.NewTimer(1 * time.Millisecond)
	for {
		select {
		case <-ts.ctx.Done():
			return
		case task := <-ts.taskC:
			current := gokcp.CurrentMS()
			if current >= task.ts {
				task.exec()
			} else {
				heap.Push(&tasks, task)
				if current < tasks[0].ts {
					updateTimer.Reset(time.Duration(tasks[0].ts-current) * time.Millisecond)
				}
			}
		case <-updateTimer.C:
			current := gokcp.CurrentMS()
			for tasks.Len() > 0 {
				if current >= tasks[0].ts {
					v := heap.Pop(&tasks).(*Task)
					v.exec()
				} else {
					if current < tasks[0].ts {
						updateTimer.Reset(time.Duration(tasks[0].ts-current) * time.Millisecond)
						break
					}
				}
			}
		}
	}
}

func (ts *TimerScheduler) pending() {
	var tasks []*Task
	for {
		select {
		case <-ts.ctx.Done():
			return
		case <-ts.taskInC:
			ts.mx.Lock()
			tasks = append(tasks, ts.pendingTasks...)
			ts.pendingTasks = ts.pendingTasks[:0]
			ts.mx.Unlock()

			for idx := range tasks {
				task := tasks[idx]
				ts.taskC <- task
			}

			tasks = nil
		}
	}
}

func (ts *TimerScheduler) PushTask(exec func(), t uint32) {
	ts.mx.Lock()
	ts.pendingTasks = append(ts.pendingTasks, &Task{exec: exec, ts: t})
	ts.mx.Unlock()

	select {
	case ts.taskInC <- struct{}{}:
	default:
	}
}
