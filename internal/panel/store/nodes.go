package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"starport-panel/internal/agent"
)

// Node 被纳管的节点。
type Node struct {
	ID           uint64      `json:"id"`
	Facts        agent.Facts `json:"facts"`
	AgentVersion string      `json:"agentVersion"`
	AgentToken   string      `json:"-"`
	Online       bool        `json:"online"`
	RegisteredAt time.Time   `json:"registeredAt"`
	LastSeenAt   time.Time   `json:"lastSeenAt"`
}

const nodeCols = `id, hostname, internal_ip, os, arch, kernel, cpu_cores, mem_bytes, cpu_used_percent, mem_used_percent,
	agent_version, agent_token, online, registered_at, last_seen_at, machine_id`

func scanNode(r interface{ Scan(...any) error }) (Node, error) {
	var n Node
	var online int
	var reg, seen string
	err := r.Scan(&n.ID, &n.Facts.Hostname, &n.Facts.InternalIP, &n.Facts.OS, &n.Facts.Arch, &n.Facts.Kernel,
		&n.Facts.CPUCores, &n.Facts.MemBytes, &n.Facts.CPUUsedPercent, &n.Facts.MemUsedPercent,
		&n.AgentVersion, &n.AgentToken, &online, &reg, &seen, &n.Facts.MachineID)
	if err != nil {
		return Node{}, err
	}
	n.Online = online == 1
	n.RegisteredAt = parseTS(reg)
	n.LastSeenAt = parseTS(seen)
	return n, nil
}

// RegisterNode 引导注册：已有节点复用原记录并换发令牌（重装 agent 不产生新节点）。
// 去重顺序：machine_id（有则唯一可靠，换 IP / 改主机名也认得出）→ hostname + internalIp（老 agent 或无 machine-id 的机器）。
func (s *Store) RegisterNode(facts agent.Facts, agentVersion string) (uint64, string, error) {
	token := newToken()
	var id uint64
	err := s.tx(context.Background(), func(tx *sql.Tx) error {
		var err error
		if facts.MachineID != "" {
			err = tx.QueryRow(`SELECT id FROM nodes WHERE machine_id = ?`, facts.MachineID).Scan(&id)
		} else {
			err = sql.ErrNoRows
		}
		if errors.Is(err, sql.ErrNoRows) {
			// 退回 hostname+ip：只匹配尚无 machine_id 的记录（或本次也没有 machine_id）；
			// 已有不同 machine_id 的同名同 IP 记录是另一台机器（克隆镜像换机），不能合并
			err = tx.QueryRow(`SELECT id FROM nodes WHERE hostname = ? AND internal_ip = ? AND (machine_id = '' OR ? = '')
				ORDER BY id LIMIT 1`, facts.Hostname, facts.InternalIP, facts.MachineID).Scan(&id)
		}
		switch {
		case err == nil:
			_, err = tx.Exec(`UPDATE nodes SET agent_token = ?, agent_version = ?, hostname = ?, internal_ip = ?, os = ?, arch = ?, kernel = ?,
				cpu_cores = ?, mem_bytes = ?, machine_id = CASE WHEN ? = '' THEN machine_id ELSE ? END WHERE id = ?`,
				token, agentVersion, facts.Hostname, facts.InternalIP, facts.OS, facts.Arch, facts.Kernel, facts.CPUCores, facts.MemBytes,
				facts.MachineID, facts.MachineID, id)
			return err
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.Exec(`INSERT INTO nodes (hostname, internal_ip, os, arch, kernel, cpu_cores, mem_bytes,
				agent_version, agent_token, registered_at, machine_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				facts.Hostname, facts.InternalIP, facts.OS, facts.Arch, facts.Kernel, facts.CPUCores, facts.MemBytes,
				agentVersion, token, now(), facts.MachineID)
			if err != nil {
				return err
			}
			last, err := res.LastInsertId()
			id = uint64(last)
			return err
		default:
			return err
		}
	})
	if err != nil {
		return 0, "", err
	}
	return id, token, nil
}

// Authenticate 令牌反查节点。
func (s *Store) Authenticate(agentToken string) (uint64, bool) {
	var id uint64
	if err := s.db.QueryRow(`SELECT id FROM nodes WHERE agent_token = ?`, agentToken).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// NodeOnline 连接就绪：回写版本/facts 并标在线。
func (s *Store) NodeOnline(nodeID uint64, agentVersion string, facts agent.Facts) {
	_, _ = s.db.Exec(`UPDATE nodes SET online = 1, agent_version = ?, hostname = ?, internal_ip = ?, os = ?, arch = ?, kernel = ?,
		cpu_cores = ?, mem_bytes = ?, cpu_used_percent = ?, mem_used_percent = ?, last_seen_at = ?,
		machine_id = CASE WHEN ? = '' THEN machine_id ELSE ? END WHERE id = ?`,
		agentVersion, facts.Hostname, facts.InternalIP, facts.OS, facts.Arch, facts.Kernel,
		facts.CPUCores, facts.MemBytes, facts.CPUUsedPercent, facts.MemUsedPercent, now(),
		facts.MachineID, facts.MachineID, nodeID)
}

// NodeHeartbeat 心跳：刷新负载与最近在线时间。
func (s *Store) NodeHeartbeat(nodeID uint64, facts agent.Facts) {
	_, _ = s.db.Exec(`UPDATE nodes SET online = 1, cpu_cores = ?, mem_bytes = ?, cpu_used_percent = ?, mem_used_percent = ?,
		last_seen_at = ? WHERE id = ?`,
		facts.CPUCores, facts.MemBytes, facts.CPUUsedPercent, facts.MemUsedPercent, now(), nodeID)
}

// NodeOffline 连接断开。
func (s *Store) NodeOffline(nodeID uint64) {
	_, _ = s.db.Exec(`UPDATE nodes SET online = 0 WHERE id = ?`, nodeID)
}

// MarkAllOffline 面板启动时调用：上次进程留下的在线标记全部作废，等 agent 重连再置回。
func (s *Store) MarkAllOffline() error {
	_, err := s.db.Exec(`UPDATE nodes SET online = 0`)
	return err
}

// ListNodes 全部节点，按 ID 升序。
func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNode 按 ID 取节点；不存在返回 ErrNotFound。
func (s *Store) GetNode(ctx context.Context, id uint64) (Node, error) {
	n, err := scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return n, err
}

// DeleteNode 删除节点记录（调用方须先确认它不属于任何集群）。不存在返回 ErrNotFound。
func (s *Store) DeleteNode(ctx context.Context, id uint64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
