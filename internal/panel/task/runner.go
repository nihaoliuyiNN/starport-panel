// Package task 是面板的异步任务执行器：对节点发起的装机 / 脚本执行在后台跑，
// 日志逐行落库供轮询，终态写回 tasks 表；支持取消。
package task

import (
	"context"
	"errors"
	"log"
	"sync"

	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/store"
)

// ErrNotRunning 任务不在运行中（已结束或不属于本进程）。
var ErrNotRunning = errors.New("task: not running")

// Fn 任务体：在 ctx 内对节点做事，逐行调用 logf；返回 agenthub.Result 作为终态。
type Fn func(ctx context.Context, logf func(string)) (agenthub.Result, error)

// OnDone 任务结束回调（在写完终态之后调用），供上层做集群状态推进。
type OnDone func(ctx context.Context, t store.Task, res agenthub.Result)

// Runner 见包注释。
type Runner struct {
	store *store.Store

	mu      sync.Mutex
	running map[uint64]context.CancelFunc
}

// New 建执行器。
func New(st *store.Store) *Runner {
	return &Runner{store: st, running: make(map[uint64]context.CancelFunc)}
}

// Start 建任务记录并在后台执行 fn，立即返回任务 ID。
// fn 返回 (res, nil) 时按 res.OK 判成功/失败；返回 context.Canceled 记 cancelled；其它 error 记 failed。
func (r *Runner) Start(kind string, nodeID, clusterID uint64, fn Fn, onDone OnDone) (uint64, error) {
	ctx, cancel := context.WithCancel(context.Background())
	id, err := r.store.CreateTask(ctx, kind, nodeID, clusterID)
	if err != nil {
		cancel()
		return 0, err
	}
	r.mu.Lock()
	r.running[id] = cancel
	r.mu.Unlock()

	go r.run(ctx, cancel, id, fn, onDone)
	return id, nil
}

func (r *Runner) run(ctx context.Context, cancel context.CancelFunc, id uint64, fn Fn, onDone OnDone) {
	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.running, id)
		r.mu.Unlock()
	}()
	logf := func(line string) {
		if err := r.store.AppendTaskLog(context.Background(), id, line); err != nil {
			log.Printf("[task] 写日志失败 task=%d: %v", id, err)
		}
	}

	res, err := fn(ctx, logf)
	bg := context.Background()
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		logf("任务已取消")
		_ = r.store.FinishTask(bg, id, store.TaskCancelled, -1, "CANCELLED", "任务被取消")
		res = agenthub.Result{OK: false, ExitCode: -1}
	case err != nil:
		code := "TASK_FAILED"
		if errors.Is(err, agenthub.ErrNodeOffline) {
			code = "NODE_AGENT_OFFLINE"
		}
		logf("任务失败: " + err.Error())
		_ = r.store.FinishTask(bg, id, store.TaskFailed, -1, code, err.Error())
		res = agenthub.Result{OK: false, ExitCode: -1}
	case res.OK:
		_ = r.store.FinishTask(bg, id, store.TaskSucceeded, res.ExitCode, "", "")
	default:
		code, msg := "EXEC_FAILED", "非零退出"
		if res.Err != nil {
			code, msg = res.Err.Code, res.Err.Message
		}
		_ = r.store.FinishTask(bg, id, store.TaskFailed, res.ExitCode, code, msg)
	}

	if onDone != nil {
		t, gerr := r.store.GetTask(bg, id)
		if gerr == nil {
			onDone(bg, t, res)
		}
	}
}

// Cancel 取消运行中的任务。
func (r *Runner) Cancel(id uint64) error {
	r.mu.Lock()
	cancel, ok := r.running[id]
	r.mu.Unlock()
	if !ok {
		return ErrNotRunning
	}
	cancel()
	return nil
}
