package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// identity 是注册后拿到的节点身份，落盘持久化，重启后直接复用（避免重复注册）。
type identity struct {
	NodeID     uint64 `json:"nodeId"`
	AgentToken string `json:"agentToken"`
	// GrpcEndpoints / GrpcTLS 注册应答下发的 gRPC 入口，随身份一起落盘，重启后不必再问一遍。
	GrpcEndpoints []string `json:"grpcEndpoints,omitempty"`
	GrpcTLS       bool     `json:"grpcTls,omitempty"`
}

const identityFile = "agent.json"

// loadIdentity 从 dataDir 读取已持久化的身份；不存在返回 ok=false。
func loadIdentity(dataDir string) (identity, bool) {
	b, err := os.ReadFile(filepath.Join(dataDir, identityFile))
	if err != nil {
		return identity{}, false
	}
	var id identity
	if err := json.Unmarshal(b, &id); err != nil || id.AgentToken == "" {
		return identity{}, false
	}
	return id, true
}

// saveIdentity 持久化身份（0600，含长期令牌，仅当前用户可读）。
func saveIdentity(dataDir string, id identity) error {
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, identityFile), b, 0o600)
}

// clearIdentity 清除已持久化身份（令牌被吊销时调用，触发重新注册）。
func clearIdentity(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, identityFile))
}
