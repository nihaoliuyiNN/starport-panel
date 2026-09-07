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
	agent_version, agent_token, online, registered_at, last_seen_at`

func scanNode(r interface{ Scan(...any) error }) (Node, error) {
	var n Node
	var online int
	var reg, seen string
	err := r.Scan(&n.ID, &n.Facts.Hostname, &n.Facts.InternalIP, &n.Facts.OS, &n.Facts.Arch, &n.Facts.Kernel,
		&n.Facts.CPUCores, &n.Facts.MemBytes, &n.Facts.CPUUsedPercent, &n.Facts.MemUsedPercent,
		&n.AgentVersion, &n.AgentToken, &online, &reg, &seen)
	if err != nil {
		return Node{}, err
	}
	n.Online = online == 1
	n.RegisteredAt = parseTS(reg)
	n.LastSeenAt = parseTS(seen)
	return n, nil
}

// RegisterNode 引导注册：同 hostname + internalIp 的节点复用原记录并换发令牌（重装 agent 不产生新节点）。
func (s *Store) RegisterNode(facts agent.Facts, agentVersion string) (uint64, string, error) {
	token := newToken()
	var id uint64
	err := s.tx(context.Background(), func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT id FROM nodes WHERE hostname = ? AND internal_ip = ?`, facts.Hostname, facts.InternalIP).Scan(&id)
		switch {
		case err == nil:
			_, err = tx.Exec(`UPDATE nodes SET agent_token = ?, agent_version = ?, os = ?, arch = ?, kernel = ?,
				cpu_cores = ?, mem_bytes = ? WHERE id = ?`,
				token, agentVersion, facts.OS, facts.Arch, facts.Kernel, facts.CPUCores, facts.MemBytes, id)
			return err
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.Exec(`INSERT INTO nodes (hostname, internal_ip, os, arch, kernel, cpu_cores, mem_bytes,
				agent_version, agent_token, registered_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				facts.Hostname, facts.InternalIP, facts.OS, facts.Arch, facts.Kernel, facts.CPUCores, facts.MemBytes,
				agentVersion, token, now())
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
		cpu_cores = ?, mem_bytes = ?, cpu_used_percent = ?, mem_used_percent = ?, last_seen_at = ? WHERE id = ?`,
		agentVersion, facts.Hostname, facts.InternalIP, facts.OS, facts.Arch, facts.Kernel,
		facts.CPUCores, facts.MemBytes, facts.CPUUsedPercent, facts.MemUsedPercent, now(), nodeID)
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

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
