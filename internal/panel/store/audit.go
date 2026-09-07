package store

import (
	"context"
	"time"
)

// AuditEntry 一次写操作的审计记录（不含请求体：可能含 kubeconfig / 令牌等敏感内容）。
type AuditEntry struct {
	ID         uint64    `json:"id"`
	At         time.Time `json:"at"`
	TokenID    uint64    `json:"tokenId,omitempty"`
	TokenName  string    `json:"tokenName"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	RemoteIP   string    `json:"remoteIp"`
	DurationMs int64     `json:"durationMs"`
}

// AppendAudit 追加审计记录。写失败只丢这一条，不影响业务请求。
func (s *Store) AppendAudit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_logs (at, token_id, token_name, method, path, status, remote_ip, duration_ms)
		VALUES (?,?,?,?,?,?,?,?)`,
		ts(e.At), e.TokenID, e.TokenName, e.Method, e.Path, e.Status, e.RemoteIP, e.DurationMs)
	return err
}

// ListAudit 最近的审计记录（ID 降序）；before>0 时只取 ID 小于 before 的（翻页），limit 默认 100、上限 1000。
func (s *Store) ListAudit(ctx context.Context, before uint64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if before == 0 {
		before = ^uint64(0) >> 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, token_id, token_name, method, path, status, remote_ip, duration_ms
		FROM audit_logs WHERE id < ? ORDER BY id DESC LIMIT ?`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.TokenID, &e.TokenName, &e.Method, &e.Path, &e.Status, &e.RemoteIP, &e.DurationMs); err != nil {
			return nil, err
		}
		e.At = parseTS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit 删除早于 before 的审计记录，返回删除条数。
func (s *Store) PruneAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_logs WHERE at < ?`, ts(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
