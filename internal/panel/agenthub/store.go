package agenthub

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"starport-panel/internal/agent"
)

// Store 是 hub 依赖的节点持久化面：注册建号、令牌反查、在线状态回写。
// hub 只关心这几件事，具体存哪（SQLite / PG / 内存）由面板装配时决定。
type Store interface {
	// RegisterNode 引导注册：按 facts 建（或按 hostname/IP 复用）节点记录，返回节点 ID 与长期凭据。
	RegisterNode(facts agent.Facts, agentVersion string) (nodeID uint64, agentToken string, err error)
	// Authenticate 长期凭据反查节点；无效返回 ok=false。
	Authenticate(agentToken string) (nodeID uint64, ok bool)
	// NodeOnline 连接就绪首帧：回写版本/facts 并标记在线。
	NodeOnline(nodeID uint64, agentVersion string, facts agent.Facts)
	// NodeHeartbeat 周期心跳：刷新 facts 与最近在线时间。
	NodeHeartbeat(nodeID uint64, facts agent.Facts)
	// NodeOffline 连接断开。
	NodeOffline(nodeID uint64)
}

// Node 内存 Store 里的节点记录（也是 Phase 0 列表接口的返回形状）。
type Node struct {
	ID           uint64      `json:"id"`
	AgentToken   string      `json:"-"`
	AgentVersion string      `json:"agentVersion"`
	Facts        agent.Facts `json:"facts"`
	Online       bool        `json:"online"`
	RegisteredAt time.Time   `json:"registeredAt"`
	LastSeenAt   time.Time   `json:"lastSeenAt"`
}

// MemoryStore 进程内实现：面板重启即丢，agent 会因 UNAUTHENTICATED 自动重注册。
// 供本地联调与 Phase 0 使用；正式存储在 Phase 1 换成 SQLite。
type MemoryStore struct {
	mu      sync.RWMutex
	nextID  uint64
	nodes   map[uint64]*Node
	byToken map[string]uint64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{nodes: make(map[uint64]*Node), byToken: make(map[string]uint64)}
}

func (s *MemoryStore) RegisterNode(facts agent.Facts, agentVersion string) (uint64, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 同一台机器重装 agent（hostname + internalIp 相同）复用原节点号，避免节点列表越来越长
	for _, n := range s.nodes {
		if n.Facts.Hostname == facts.Hostname && n.Facts.InternalIP == facts.InternalIP {
			delete(s.byToken, n.AgentToken)
			n.AgentToken = newToken()
			n.AgentVersion = agentVersion
			n.Facts = facts
			s.byToken[n.AgentToken] = n.ID
			return n.ID, n.AgentToken, nil
		}
	}
	s.nextID++
	n := &Node{ID: s.nextID, AgentToken: newToken(), AgentVersion: agentVersion, Facts: facts, RegisteredAt: time.Now()}
	s.nodes[n.ID] = n
	s.byToken[n.AgentToken] = n.ID
	return n.ID, n.AgentToken, nil
}

func (s *MemoryStore) Authenticate(agentToken string) (uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byToken[agentToken]
	return id, ok
}

func (s *MemoryStore) NodeOnline(nodeID uint64, agentVersion string, facts agent.Facts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.nodes[nodeID]; n != nil {
		n.Online = true
		n.AgentVersion = agentVersion
		n.Facts = facts
		n.LastSeenAt = time.Now()
	}
}

func (s *MemoryStore) NodeHeartbeat(nodeID uint64, facts agent.Facts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.nodes[nodeID]; n != nil {
		n.Online = true
		n.Facts = facts
		n.LastSeenAt = time.Now()
	}
}

func (s *MemoryStore) NodeOffline(nodeID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.nodes[nodeID]; n != nil {
		n.Online = false
	}
}

// List 全部节点（按 ID 升序）。
func (s *MemoryStore) List() []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Node, 0, len(s.nodes))
	for id := uint64(1); id <= s.nextID; id++ {
		if n := s.nodes[id]; n != nil {
			out = append(out, *n)
		}
	}
	return out
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
