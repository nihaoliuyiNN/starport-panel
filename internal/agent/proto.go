// Package agent 是 starport-agent（节点侧代理）的实现：驻在【裸机 / 手动纳管的物理机或 ECS】上，
// 主动呼出连控制面，替代原来「控制面 SSH 进机器跑 kubeadm 脚本」的装机方式。
//
// 设计要点：
//   - 呼出而非被连：agent 只对控制面发起出站连接，机器上不监听任何端口、不保管 SSH 凭证，
//     攻击面比 SSH 小得多，天然穿 NAT / 防火墙。
//   - 传输层是 gRPC 双向流（Nacos 2.x 同款模型）：一条长连，控制面沿同一条流反向下发指令。
//     线上契约唯一来源是 proto/agent/v1/agent.proto，生成物在 internal/pb/agentv1。
//
// ── 控制面端点 ─────────────────────────────────────────────────────────────
//
//  1. 引导注册  POST {panel}/api/v1/agents/register   （HTTP JSON）
//     Header: X-Starport-Bootstrap-Token: <一次性引导令牌>
//     Body:   RegisterRequest{ facts, agentVersion }
//     200:    RegisterResponse{ nodeId, agentToken, heartbeatIntervalMs, grpcEndpoints, grpcTls }
//     语义：校验引导令牌 → 建/取节点记录 → 下发长期 agentToken（可单独吊销）、心跳节奏与 gRPC 入口。
//
//  2. 呼出长连  NodeAgentService.Connect（gRPC 双向流，面板独立端口）
//     metadata: x-starport-agent-token / x-starport-node-id / x-starport-agent-version
//     令牌失效/被吊销时服务端返回 UNAUTHENTICATED，agent 清本地身份重新走注册。
//
// ── 流上的帧（见 .proto）────────────────────────────────────────────────────
//
//	控制面 → agent (ServerFrame)：exec / install / cancel / ping / session_open|stdin|resize|close
//	agent → 控制面 (AgentFrame)：ready / heartbeat / log / result / pong / session_data|exit
//
// 会话与 exec **不共用执行通道**：exec 走串行队列且持全局互斥锁（避免 apt/kubeadm 抢锁），
// 而会话可能持续几十分钟；若混用，一个终端窗口就会把这台机器上所有 exec 堵死。
// 因此 session_* 在 readLoop 里直接起独立 goroutine 处理，各会话之间也互不阻塞。
package agent

import (
	"starport-panel/internal/installer"
	pb "starport-panel/internal/pb/agentv1"
)

// HTTP 注册使用的请求头。
const (
	// HeaderBootstrapToken 一次性引导令牌，仅注册时用；换到 agentToken 后不再使用。
	HeaderBootstrapToken = "X-Starport-Bootstrap-Token"
	// HeaderAgentVersion starport-agent 自身版本。
	HeaderAgentVersion = "X-Starport-Agent-Version"
)

// gRPC 长连握手的 metadata 键（gRPC 要求小写）。
const (
	MetaAgentToken   = "x-starport-agent-token"
	MetaNodeID       = "x-starport-node-id"
	MetaAgentVersion = "x-starport-agent-version"
)

// DefaultGrpcPort 注册应答未下发 grpcEndpoints 时的兜底端口（面板 HTTP 8080 → gRPC 9192，
// 与 starport-panel serve 的 --grpc 默认值一致）。
const DefaultGrpcPort = "9192"

// Error 结构化错误：控制面按 code 分支/重试，不解析字符串。
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// 执行相关的错误码。
const (
	ErrExecTimeout       = "EXEC_TIMEOUT"        // 脚本超时被杀
	ErrExecFailed        = "EXEC_FAILED"         // 脚本非零退出或启动失败
	ErrCancelled         = "CANCELLED"           // 被控制面取消
	ErrScriptWriteFailed = "SCRIPT_WRITE_FAILED" // 落临时脚本文件失败
)

// 节点角色（装机域定义在 installer 包，这里对齐引用，便于协议侧使用）。
const (
	RoleFirstMaster = installer.RoleFirstMaster
	RoleJoinMaster  = installer.RoleJoinMaster
	RoleWorker      = installer.RoleWorker
)

// Facts 主机现状：HTTP 注册时以 JSON 上报；ready / 心跳时转 pb.Facts 沿流上报。
// JSON 字段名与 .proto 的 json_name 一致，Java 侧一份 record 两用。
type Facts struct {
	Hostname   string `json:"hostname"`
	InternalIP string `json:"internalIp"`
	OS         string `json:"os"`   // linux
	Arch       string `json:"arch"` // amd64 / arm64
	Kernel     string `json:"kernel,omitempty"`
	CPUCores   int    `json:"cpuCores"`
	MemBytes   uint64 `json:"memBytes"`
	// 实时利用率（心跳每次刷新）：CPU 取 /proc/stat 两次采样差值，内存取 (MemTotal-MemAvailable)/MemTotal。
	// 平台以此展示节点负载，取代云厂商监控插件。
	CPUUsedPercent float64 `json:"cpuUsedPercent"`
	MemUsedPercent float64 `json:"memUsedPercent"`
	// MachineID /etc/machine-id（Linux），面板注册去重用；无则空。
	MachineID string `json:"machineId,omitempty"`
}

// RegisterRequest 引导注册请求体。
type RegisterRequest struct {
	Facts        Facts  `json:"facts"`
	AgentVersion string `json:"agentVersion"`
}

// RegisterResponse 引导注册应答：下发节点身份、心跳节奏与 gRPC 入口。
type RegisterResponse struct {
	NodeID              uint64 `json:"nodeId"`
	AgentToken          string `json:"agentToken"`
	HeartbeatIntervalMs int64  `json:"heartbeatIntervalMs"`
	// GrpcEndpoints 控制面 gRPC 入口列表（host:port），agent 轮询尝试；空则按 server 主机 + 9192 兜底。
	GrpcEndpoints []string `json:"grpcEndpoints,omitempty"`
	// GrpcTLS 入口是否要求 TLS。
	GrpcTLS bool `json:"grpcTls,omitempty"`
}

// 装机域类型定义在 installer 包（自洽、不反向依赖 agent）；协议层以别名复用。
type (
	InstallSpec   = installer.InstallSpec
	InstallResult = installer.InstallResult
)

// ── pb ↔ 领域类型转换（installer 包保持 stdlib-only，不直接依赖生成代码）──────

func (f Facts) toPB() *pb.Facts {
	return &pb.Facts{
		Hostname:       f.Hostname,
		InternalIp:     f.InternalIP,
		Os:             f.OS,
		Arch:           f.Arch,
		Kernel:         f.Kernel,
		CpuCores:       int32(f.CPUCores),
		MemBytes:       f.MemBytes,
		CpuUsedPercent: f.CPUUsedPercent,
		MemUsedPercent: f.MemUsedPercent,
		MachineId:      f.MachineID,
	}
}

func (e *Error) toPB() *pb.Error {
	if e == nil {
		return nil
	}
	return &pb.Error{Code: e.Code, Message: e.Message, Retryable: e.Retryable}
}

func failToError(f *installer.Fail) *Error {
	if f == nil {
		return nil
	}
	return &Error{Code: f.Code, Message: f.Message, Retryable: f.Retryable}
}

func roleFromPB(r pb.NodeRole) string {
	switch r {
	case pb.NodeRole_NODE_ROLE_FIRST_MASTER:
		return installer.RoleFirstMaster
	case pb.NodeRole_NODE_ROLE_JOIN_MASTER:
		return installer.RoleJoinMaster
	case pb.NodeRole_NODE_ROLE_WORKER:
		return installer.RoleWorker
	default:
		return ""
	}
}

func cniFromPB(t pb.CniType) string {
	switch t {
	case pb.CniType_CNI_TYPE_CILIUM:
		return installer.CNICilium
	case pb.CniType_CNI_TYPE_CALICO:
		return installer.CNICalico
	default:
		return ""
	}
}

func artifactModeFromPB(m pb.ArtifactMode) string {
	switch m {
	case pb.ArtifactMode_ARTIFACT_MODE_BUNDLE:
		return installer.ArtifactBundle
	case pb.ArtifactMode_ARTIFACT_MODE_ONLINE:
		return installer.ArtifactOnline
	default:
		return ""
	}
}

// installSpecFromPB 把线上 InstallSpec 翻成 installer 领域类型；nil 返回 nil。
func installSpecFromPB(p *pb.InstallSpec) *InstallSpec {
	if p == nil {
		return nil
	}
	s := &InstallSpec{
		Role:                 roleFromPB(p.GetRole()),
		K8sVersion:           p.GetK8SVersion(),
		PodCIDR:              p.GetPodCidr(),
		ServiceCIDR:          p.GetServiceCidr(),
		ImageRepository:      p.GetImageRepository(),
		ControlPlaneEndpoint: p.GetControlPlaneEndpoint(),
		AdvertiseAddress:     p.GetAdvertiseAddress(),
		CertSANs:             p.GetCertSans(),
		Addons:               p.GetAddons(),
	}
	if v := p.GetVip(); v != nil {
		s.VIP = &installer.VIPSpec{Enabled: v.GetEnabled(), Address: v.GetAddress(), Interface: v.GetInterface(), Version: v.GetVersion()}
	}
	if c := p.GetCni(); c != nil {
		s.CNI = &installer.CNISpec{Type: cniFromPB(c.GetType()), Version: c.GetVersion()}
	}
	if j := p.GetJoin(); j != nil {
		s.Join = &installer.JoinSpec{Token: j.GetToken(), CACertHash: j.GetCaCertHash(), CertificateKey: j.GetCertificateKey()}
	}
	if a := p.GetArtifact(); a != nil {
		s.Artifact = &installer.ArtifactSpec{Mode: artifactModeFromPB(a.GetMode()), BundleURL: a.GetBundleUrl(), UseCNMirror: a.GetUseCnMirror()}
	}
	return s
}

// installResultToPB 装机终态 → 线上类型；nil 返回 nil。
func installResultToPB(r *InstallResult) *pb.InstallResult {
	if r == nil {
		return nil
	}
	return &pb.InstallResult{
		JoinCommand:          r.JoinCommand,
		Token:                r.Token,
		CaCertHash:           r.CACertHash,
		CertificateKey:       r.CertificateKey,
		ControlPlaneEndpoint: r.ControlPlaneEndpoint,
		KubeconfigB64:        r.KubeconfigB64,
	}
}
