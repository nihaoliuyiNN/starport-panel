package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// 集群状态。
const (
	ClusterCreated    = "created"    // 已建记录，尚无控制面
	ClusterInstalling = "installing" // 首 master 装机中
	ClusterReady      = "ready"      // 控制面可用，可加节点
	ClusterFailed     = "failed"     // 首 master 装机失败，可重试
)

// 集群内节点状态。
const (
	MemberInstalling = "installing"
	MemberReady      = "ready"
	MemberFailed     = "failed"
)

// Cluster 集群记录。凭据类字段不序列化到 JSON。
type Cluster struct {
	ID                   uint64    `json:"id"`
	Name                 string    `json:"name"`
	K8sVersion           string    `json:"k8sVersion"`
	PodCIDR              string    `json:"podCIDR"`
	ServiceCIDR          string    `json:"serviceCIDR"`
	ControlPlaneEndpoint string    `json:"controlPlaneEndpoint"` // host:port；空表示由首 master IP 推导
	VIP                  string    `json:"vip,omitempty"`
	VIPInterface         string    `json:"vipInterface,omitempty"`
	CNI                  string    `json:"cni"`
	CNIVersion           string    `json:"cniVersion,omitempty"`
	Addons               []string  `json:"addons"`
	ArtifactMode         string    `json:"artifactMode"`
	BundleURL            string    `json:"bundleUrl,omitempty"`
	UseCNMirror          bool      `json:"useCnMirror"`
	Status               string    `json:"status"`
	Kubeconfig           string    `json:"-"`
	Join                 JoinCreds `json:"-"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// JoinCreds 加入集群的凭据（首 master 装机回传 / 后续刷新）。
type JoinCreds struct {
	Token          string
	CACertHash     string
	CertificateKey string
	IssuedAt       time.Time
}

// Member 集群内的一个节点。
type Member struct {
	ClusterID uint64    `json:"clusterId"`
	NodeID    uint64    `json:"nodeId"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	TaskID    uint64    `json:"taskId,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const clusterCols = `id, name, k8s_version, pod_cidr, service_cidr, control_plane_endpoint, vip, vip_interface,
	cni, cni_version, addons, artifact_mode, bundle_url, use_cn_mirror, status, kubeconfig,
	join_token, join_ca_cert_hash, join_certificate_key, join_issued_at, created_at, updated_at`

func scanCluster(r interface{ Scan(...any) error }) (Cluster, error) {
	var c Cluster
	var addons, issued, created, updated string
	var cn int
	err := r.Scan(&c.ID, &c.Name, &c.K8sVersion, &c.PodCIDR, &c.ServiceCIDR, &c.ControlPlaneEndpoint, &c.VIP, &c.VIPInterface,
		&c.CNI, &c.CNIVersion, &addons, &c.ArtifactMode, &c.BundleURL, &cn, &c.Status, &c.Kubeconfig,
		&c.Join.Token, &c.Join.CACertHash, &c.Join.CertificateKey, &issued, &created, &updated)
	if err != nil {
		return Cluster{}, err
	}
	c.UseCNMirror = cn == 1
	c.Addons = []string{}
	_ = json.Unmarshal([]byte(addons), &c.Addons)
	c.Join.IssuedAt = parseTS(issued)
	c.CreatedAt = parseTS(created)
	c.UpdatedAt = parseTS(updated)
	return c, nil
}

// CreateCluster 新建集群记录（status=created），回填 ID / 时间。
func (s *Store) CreateCluster(ctx context.Context, c *Cluster) error {
	addons, _ := json.Marshal(c.Addons)
	if c.Addons == nil {
		addons = []byte("[]")
	}
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO clusters (name, k8s_version, pod_cidr, service_cidr, control_plane_endpoint,
		vip, vip_interface, cni, cni_version, addons, artifact_mode, bundle_url, use_cn_mirror, status, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.Name, c.K8sVersion, c.PodCIDR, c.ServiceCIDR, c.ControlPlaneEndpoint,
		c.VIP, c.VIPInterface, c.CNI, c.CNIVersion, string(addons), c.ArtifactMode, c.BundleURL, b2i(c.UseCNMirror),
		ClusterCreated, t, t)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	c.ID = uint64(id)
	c.Status = ClusterCreated
	c.CreatedAt, c.UpdatedAt = parseTS(t), parseTS(t)
	return nil
}

// ListClusters 全部集群。
func (s *Store) ListClusters(ctx context.Context) ([]Cluster, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+clusterCols+` FROM clusters ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cluster
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCluster 按 ID 取集群；不存在返回 ErrNotFound。
func (s *Store) GetCluster(ctx context.Context, id uint64) (Cluster, error) {
	c, err := scanCluster(s.db.QueryRowContext(ctx, `SELECT `+clusterCols+` FROM clusters WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Cluster{}, ErrNotFound
	}
	return c, err
}

// SetClusterStatus 更新状态。
func (s *Store) SetClusterStatus(ctx context.Context, id uint64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE clusters SET status = ?, updated_at = ? WHERE id = ?`, status, now(), id)
	return err
}

// SetClusterEndpoint 回填控制面入口（首 master 装机时由其 IP 推导的场景）。
func (s *Store) SetClusterEndpoint(ctx context.Context, id uint64, endpoint string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE clusters SET control_plane_endpoint = ?, updated_at = ? WHERE id = ?`, endpoint, now(), id)
	return err
}

// SetClusterCredentials 首 master 装机成功：写入 kubeconfig 与 join 凭据，状态置 ready。
func (s *Store) SetClusterCredentials(ctx context.Context, id uint64, kubeconfig string, join JoinCreds) error {
	_, err := s.db.ExecContext(ctx, `UPDATE clusters SET kubeconfig = ?, join_token = ?, join_ca_cert_hash = ?, join_certificate_key = ?,
		join_issued_at = ?, status = ?, updated_at = ? WHERE id = ?`,
		kubeconfig, join.Token, join.CACertHash, join.CertificateKey, ts(join.IssuedAt), ClusterReady, now(), id)
	return err
}

// SetJoinCreds 刷新 join 凭据（token 24h / certificateKey 2h 过期后由面板在 master 上重新签发）。
func (s *Store) SetJoinCreds(ctx context.Context, id uint64, join JoinCreds) error {
	_, err := s.db.ExecContext(ctx, `UPDATE clusters SET join_token = ?, join_ca_cert_hash = ?, join_certificate_key = ?,
		join_issued_at = ?, updated_at = ? WHERE id = ?`,
		join.Token, join.CACertHash, join.CertificateKey, ts(join.IssuedAt), now(), id)
	return err
}

// UpsertMember 写入/更新集群成员状态。
func (s *Store) UpsertMember(ctx context.Context, m Member) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cluster_nodes (cluster_id, node_id, role, status, error, task_id, updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(cluster_id, node_id) DO UPDATE SET role = excluded.role, status = excluded.status,
		error = excluded.error, task_id = excluded.task_id, updated_at = excluded.updated_at`,
		m.ClusterID, m.NodeID, m.Role, m.Status, m.Error, m.TaskID, now())
	return err
}

// SetMemberTask 只回填成员的任务 ID（状态由任务终态回调推进，二者分开写避免互相覆盖）。
func (s *Store) SetMemberTask(ctx context.Context, clusterID, nodeID, taskID uint64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cluster_nodes SET task_id = ? WHERE cluster_id = ? AND node_id = ?`, taskID, clusterID, nodeID)
	return err
}

// SetMemberStatus 推进成员状态（error 为空表示清除）。
func (s *Store) SetMemberStatus(ctx context.Context, clusterID, nodeID uint64, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cluster_nodes SET status = ?, error = ?, updated_at = ? WHERE cluster_id = ? AND node_id = ?`,
		status, errMsg, now(), clusterID, nodeID)
	return err
}

// ListMembers 集群成员，按加入顺序。
func (s *Store) ListMembers(ctx context.Context, clusterID uint64) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cluster_id, node_id, role, status, error, task_id, updated_at
		FROM cluster_nodes WHERE cluster_id = ? ORDER BY updated_at, node_id`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var upd string
		if err := rows.Scan(&m.ClusterID, &m.NodeID, &m.Role, &m.Status, &m.Error, &m.TaskID, &upd); err != nil {
			return nil, err
		}
		m.UpdatedAt = parseTS(upd)
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMember 取某节点在集群中的成员记录；不存在返回 ErrNotFound。
func (s *Store) GetMember(ctx context.Context, clusterID, nodeID uint64) (Member, error) {
	var m Member
	var upd string
	err := s.db.QueryRowContext(ctx, `SELECT cluster_id, node_id, role, status, error, task_id, updated_at
		FROM cluster_nodes WHERE cluster_id = ? AND node_id = ?`, clusterID, nodeID).
		Scan(&m.ClusterID, &m.NodeID, &m.Role, &m.Status, &m.Error, &m.TaskID, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	m.UpdatedAt = parseTS(upd)
	return m, err
}

// NodeMemberships 某节点所属的全部集群成员记录（一台机只能属于一个集群，用于加入前校验）。
func (s *Store) NodeMemberships(ctx context.Context, nodeID uint64) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cluster_id, node_id, role, status, error, task_id, updated_at
		FROM cluster_nodes WHERE node_id = ?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var upd string
		if err := rows.Scan(&m.ClusterID, &m.NodeID, &m.Role, &m.Status, &m.Error, &m.TaskID, &upd); err != nil {
			return nil, err
		}
		m.UpdatedAt = parseTS(upd)
		out = append(out, m)
	}
	return out, rows.Err()
}
