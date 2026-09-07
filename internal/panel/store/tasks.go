package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 任务类型与状态。
const (
	TaskInstall = "install"
	TaskExec    = "exec"

	TaskRunning   = "running"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

// Task 面板对节点发起的一次异步动作。
type Task struct {
	ID           uint64     `json:"id"`
	Kind         string     `json:"kind"`
	NodeID       uint64     `json:"nodeId"`
	ClusterID    uint64     `json:"clusterId,omitempty"`
	Status       string     `json:"status"`
	ExitCode     int        `json:"exitCode"`
	ErrorCode    string     `json:"errorCode,omitempty"`
	ErrorMessage string     `json:"errorMessage,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

// Done 是否已到终态。
func (t Task) Done() bool { return t.Status != TaskRunning }

// TaskLog 一行任务日志。
type TaskLog struct {
	Seq  int64     `json:"seq"`
	At   time.Time `json:"at"`
	Line string    `json:"line"`
}

const taskCols = `id, kind, node_id, cluster_id, status, exit_code, error_code, error_message, started_at, finished_at`

func scanTask(r interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var started, finished string
	err := r.Scan(&t.ID, &t.Kind, &t.NodeID, &t.ClusterID, &t.Status, &t.ExitCode, &t.ErrorCode, &t.ErrorMessage, &started, &finished)
	if err != nil {
		return Task{}, err
	}
	t.StartedAt = parseTS(started)
	if finished != "" {
		f := parseTS(finished)
		t.FinishedAt = &f
	}
	return t, nil
}

// CreateTask 新建 running 任务，返回 ID。
func (s *Store) CreateTask(ctx context.Context, kind string, nodeID, clusterID uint64) (uint64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO tasks (kind, node_id, cluster_id, status, started_at) VALUES (?,?,?,?,?)`,
		kind, nodeID, clusterID, TaskRunning, now())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return uint64(id), nil
}

// FinishTask 写终态。
func (s *Store) FinishTask(ctx context.Context, id uint64, status string, exitCode int, errCode, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, exit_code = ?, error_code = ?, error_message = ?, finished_at = ?
		WHERE id = ?`, status, exitCode, errCode, errMsg, now(), id)
	return err
}

// FailRunningTasks 面板启动时调用：上次进程留下的 running 任务已无人跟踪，统一判失败。
func (s *Store) FailRunningTasks(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, error_code = 'PANEL_RESTARTED',
		error_message = '面板重启，任务状态丢失', finished_at = ? WHERE status = ?`, TaskFailed, now(), TaskRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetTask 按 ID 取任务；不存在返回 ErrNotFound。
func (s *Store) GetTask(ctx context.Context, id uint64) (Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// ListTasks 最近任务（倒序）；nodeID=0 表示全部。
func (s *Store) ListTasks(ctx context.Context, nodeID uint64, limit int) ([]Task, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + taskCols + ` FROM tasks WHERE (? = 0 OR node_id = ?) ORDER BY id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, nodeID, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AppendTaskLog 追加一行日志，seq 自增。
func (s *Store) AppendTaskLog(ctx context.Context, taskID uint64, line string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_logs (task_id, seq, at, line)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM task_logs WHERE task_id = ?), ?, ?)`,
		taskID, taskID, now(), line)
	return err
}

// TaskLogs 取 seq > after 的日志（最多 limit 行），供轮询增量拉取。
func (s *Store) TaskLogs(ctx context.Context, taskID uint64, after int64, limit int) ([]TaskLog, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, at, line FROM task_logs WHERE task_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskLog{}
	for rows.Next() {
		var l TaskLog
		var at string
		if err := rows.Scan(&l.Seq, &at, &l.Line); err != nil {
			return nil, err
		}
		l.At = parseTS(at)
		out = append(out, l)
	}
	return out, rows.Err()
}
