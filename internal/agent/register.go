package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// errUnauthorized 令牌无效/被吊销（注册或连接返回 401）：上层据此清身份重注册。
var errUnauthorized = errors.New("agent: unauthorized (token invalid or revoked)")

// RegisterPath 引导注册的 HTTP 路径（面板侧 agenthub 同名常量对齐）。
const RegisterPath = "/api/v1/agents/register"

// register 用引导令牌换取节点身份。
func register(cfg Config, facts Facts) (RegisterResponse, error) {
	body, _ := json.Marshal(RegisterRequest{Facts: facts, AgentVersion: cfg.AgentVersion})
	req, err := http.NewRequest(http.MethodPost, cfg.ServerURL+RegisterPath, bytes.NewReader(body))
	if err != nil {
		return RegisterResponse{}, err
	}
	req.Header.Set(HeaderBootstrapToken, cfg.BootstrapToken)
	req.Header.Set(HeaderAgentVersion, cfg.AgentVersion)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return RegisterResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return RegisterResponse{}, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return RegisterResponse{}, fmt.Errorf("register status %d: %s", resp.StatusCode, b)
	}

	var out RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return RegisterResponse{}, err
	}
	if out.NodeID == 0 || out.AgentToken == "" {
		return RegisterResponse{}, errors.New("register: empty nodeId or agentToken")
	}
	return out, nil
}
