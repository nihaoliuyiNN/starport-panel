package agenthub

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net"
	"net/http"

	"starport-panel/internal/agent"
)

// RegisterPath 引导注册路径，与 agent 侧常量对齐。
const RegisterPath = agent.RegisterPath

// RegisterHandler 处理 POST /api/v1/agents/register：校验引导令牌 → 建节点 → 下发长期凭据与 gRPC 入口。
func (h *Hub) RegisterHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		got := r.Header.Get(agent.HeaderBootstrapToken)
		if h.opts.BootstrapToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(h.opts.BootstrapToken)) != 1 {
			http.Error(w, `{"error":"引导令牌无效"}`, http.StatusUnauthorized)
			return
		}
		var req agent.RegisterRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			http.Error(w, `{"error":"请求体不是合法 JSON"}`, http.StatusBadRequest)
			return
		}
		if req.AgentVersion == "" {
			req.AgentVersion = r.Header.Get(agent.HeaderAgentVersion)
		}
		nodeID, token, err := h.store.RegisterNode(req.Facts, req.AgentVersion)
		if err != nil {
			log.Printf("[agenthub] 注册失败 host=%s: %v", req.Facts.Hostname, err)
			http.Error(w, `{"error":"注册失败"}`, http.StatusInternalServerError)
			return
		}
		log.Printf("[agenthub] 注册成功 nodeId=%d host=%s ip=%s version=%s",
			nodeID, req.Facts.Hostname, req.Facts.InternalIP, req.AgentVersion)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agent.RegisterResponse{
			NodeID:              nodeID,
			AgentToken:          token,
			HeartbeatIntervalMs: h.opts.HeartbeatInterval.Milliseconds(),
			GrpcEndpoints:       h.grpcEndpoints(r),
			GrpcTLS:             h.opts.GrpcTLS,
		})
	})
}

// grpcEndpoints 显式配置优先；否则用 agent 访问本次注册所用的主机 + gRPC 端口——
// agent 能用这个主机名打到 HTTP，通常也能打到同机的 gRPC 端口。
func (h *Hub) grpcEndpoints(r *http.Request) []string {
	if len(h.opts.GrpcEndpoints) > 0 {
		return h.opts.GrpcEndpoints
	}
	host := r.Host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		host = hp
	}
	return []string{net.JoinHostPort(host, h.opts.GrpcPort)}
}
