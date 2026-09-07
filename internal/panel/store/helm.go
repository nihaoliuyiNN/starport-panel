package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HelmRepo 某集群配置的 Helm 仓库。密码不出 JSON。
type HelmRepo struct {
	ID        uint64    `json:"id"`
	ClusterID uint64    `json:"clusterId"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Username  string    `json:"username,omitempty"`
	Password  string    `json:"-"`
	HasAuth   bool      `json:"hasAuth"`
	CreatedAt time.Time `json:"createdAt"`
}

const helmRepoCols = `id, cluster_id, name, url, username, password, created_at`

func scanHelmRepo(r interface{ Scan(...any) error }) (HelmRepo, error) {
	var h HelmRepo
	var created string
	if err := r.Scan(&h.ID, &h.ClusterID, &h.Name, &h.URL, &h.Username, &h.Password, &created); err != nil {
		return HelmRepo{}, err
	}
	h.HasAuth = h.Username != "" || h.Password != ""
	h.CreatedAt = parseTS(created)
	return h, nil
}

// AddHelmRepo 新增仓库；同集群同名冲突返回唯一约束错误（IsConflict）。
func (s *Store) AddHelmRepo(ctx context.Context, h *HelmRepo) error {
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO helm_repos (cluster_id, name, url, username, password, created_at) VALUES (?,?,?,?,?,?)`,
		h.ClusterID, h.Name, h.URL, h.Username, h.Password, t)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	h.ID = uint64(id)
	h.HasAuth = h.Username != "" || h.Password != ""
	h.CreatedAt = parseTS(t)
	return nil
}

// ListHelmRepos 某集群全部仓库。
func (s *Store) ListHelmRepos(ctx context.Context, clusterID uint64) ([]HelmRepo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+helmRepoCols+` FROM helm_repos WHERE cluster_id = ? ORDER BY name`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HelmRepo
	for rows.Next() {
		h, err := scanHelmRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHelmRepo 按集群 + 名称取仓库；不存在返回 ErrNotFound。
func (s *Store) GetHelmRepo(ctx context.Context, clusterID uint64, name string) (HelmRepo, error) {
	h, err := scanHelmRepo(s.db.QueryRowContext(ctx, `SELECT `+helmRepoCols+` FROM helm_repos WHERE cluster_id = ? AND name = ?`, clusterID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return HelmRepo{}, ErrNotFound
	}
	return h, err
}

// DeleteHelmRepo 删除仓库；不存在返回 ErrNotFound。
func (s *Store) DeleteHelmRepo(ctx context.Context, clusterID uint64, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM helm_repos WHERE cluster_id = ? AND name = ?`, clusterID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
