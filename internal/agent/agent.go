// agent.go 是 starport-agent 的编排入口：注册换身份 → 维持呼出会话（断线指数退避重连，永不放弃）。
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// Config starport-agent 启动参数。
type Config struct {
	ServerURL      string // 控制面 HTTP 地址（注册 / 下载二进制经网关），如 https://console.example.com
	BootstrapToken string // 一次性引导令牌，仅首次注册用
	DataDir        string // 身份持久化与临时脚本目录，默认 /var/lib/starport-agent
	Shell          string // 执行脚本的 shell，默认 /bin/bash
	AgentVersion   string // 自身版本
	HeartbeatMs    int64  // 兜底心跳间隔（注册应答未给时用），默认 15000
	// GrpcEndpoints 可选：显式指定控制面 gRPC 入口（逗号分隔 host:port），覆盖注册应答下发的值。
	GrpcEndpoints string
	// GrpcTLS 可选：强制 gRPC 走 TLS（注册应答 grpcTls=true 时自动开启，此项用于覆盖）。
	GrpcTLS bool
}

func (c Config) heartbeatInterval() time.Duration {
	if c.HeartbeatMs > 0 {
		return time.Duration(c.HeartbeatMs) * time.Millisecond
	}
	return 15 * time.Second
}

// Run 启动 starport-agent，阻塞运行直到 ctx 取消。
func Run(ctx context.Context, cfg Config) error {
	if cfg.ServerURL == "" {
		return errors.New("starport-agent: ServerURL 必填")
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/starport-agent"
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}

	exectr := NewExecutor(cfg.Shell, cfg.DataDir)
	idem := newIdempotentStore(idempotentTTL)

	// 1) 拿身份：优先复用已持久化的；没有则用引导令牌注册（瞬时故障重试，密钥错则退出）
	id, ok := loadIdentity(cfg.DataDir)
	if !ok {
		var err error
		var hb int64
		id, hb, err = registerLoop(ctx, cfg)
		if err != nil {
			return err // 仅 ctx 取消才会走到这
		}
		if hb > 0 {
			cfg.HeartbeatMs = hb
		}
	}
	log.Printf("[agent] 身份就绪 nodeId=%d，连接控制面 %s", id.NodeID, cfg.ServerURL)

	// 2) 呼出会话循环：断线指数退避重连（1s→30s 封顶，稳定在线 1 分钟后重置）；
	//    多个 gRPC 入口按 attempt 轮转，单个入口不可达不会卡死。
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	attempt := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := time.Now()
		err := runSession(ctx, cfg, id, exectr, idem, attempt)
		attempt++

		// 令牌失效/被吊销：清身份，用引导令牌重新注册后再连
		if errors.Is(err, errUnauthorized) {
			log.Printf("[agent] 令牌失效，清除身份并重新注册")
			clearIdentity(cfg.DataDir)
			newID, hb, rerr := registerLoop(ctx, cfg)
			if rerr != nil {
				return rerr
			}
			id = newID
			if hb > 0 {
				cfg.HeartbeatMs = hb
			}
			backoff = time.Second
			continue
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("[agent] 会话结束: %v；%s 后重连", err, backoff)
		}

		if time.Since(start) > time.Minute {
			backoff = time.Second // 稳定在线过后重置退避
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// registerLoop 注册直到成功或 ctx 取消（瞬时故障如网络/5xx 每 3 秒重试）。
// 引导令牌无效（服务端 401/403）视为不可恢复：直接返回错误让进程退出——
// “密钥错了连 Agent 都起不来”，避免用错密钥的节点空转重试、伪装成在装。
// 返回节点身份与控制面下发的心跳间隔（ms，未给时为 0）。
func registerLoop(ctx context.Context, cfg Config) (identity, int64, error) {
	facts := collectFacts()
	for {
		if ctx.Err() != nil {
			return identity{}, 0, ctx.Err()
		}
		resp, err := register(cfg, facts)
		if err == nil {
			id := identity{NodeID: resp.NodeID, AgentToken: resp.AgentToken,
				GrpcEndpoints: resp.GrpcEndpoints, GrpcTLS: resp.GrpcTLS}
			if serr := saveIdentity(cfg.DataDir, id); serr != nil {
				log.Printf("[agent] 持久化身份失败（继续运行，重启需重注册）: %v", serr)
			}
			log.Printf("[agent] 注册成功 nodeId=%d", id.NodeID)
			return id, resp.HeartbeatIntervalMs, nil
		}
		// 密钥错误：不可恢复，立即失败退出（不重试）
		if errors.Is(err, errUnauthorized) {
			return identity{}, 0, fmt.Errorf("引导令牌无效，服务端拒绝注册（请检查 --token / STARPORT_BOOTSTRAP_TOKEN）: %w", err)
		}
		log.Printf("[agent] 注册失败: %v；3s 后重试", err)
		select {
		case <-ctx.Done():
			return identity{}, 0, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
