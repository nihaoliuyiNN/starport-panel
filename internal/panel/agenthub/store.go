package agenthub

import "starport-panel/internal/agent"

// Store 是 hub 依赖的节点持久化面：注册建号、令牌反查、在线状态回写。
// hub 只关心这几件事，具体实现在 internal/panel/store（SQLite）。
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
