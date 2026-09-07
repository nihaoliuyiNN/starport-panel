# starport-panel

自托管的 Kubernetes 面板：一个 Go 二进制起控制面，往裸机 / 物理机 / 云主机上装 `starport-agent`，
面板经 agent 把 Kubernetes（kubeadm HA + kube-vip + Cilium/Calico）装起来，再管理集群与应用。

- **单二进制、零依赖**：面板一个进程，节点侧一个静态二进制；没有 SSH、节点不开端口、天然穿 NAT。
- **离线优先**：装机制品（kube 二进制、containerd、CNI、镜像）打成离线包，节点无需外网；也支持在线模式 + 国内镜像源。
- **结构化装机**：面板下发的是结构化 `InstallSpec`，不是一坨脚本；预检 / 阶段 / 失败码都是数据，可重试、可断点。
- **契约先行**：面板 ↔ agent 走 gRPC 双向流，`.proto` 是唯一事实来源。

> 当前状态：**Phase 1 —— 节点纳管 + 装集群**。应用管理、Web UI 见下方路线图。

## 组成

| 目录 | 内容 |
|---|---|
| `cmd/starport-panel` | 控制面进程：HTTP API（:8080）+ agent gRPC 入口（:9192） |
| `cmd/starport-agent` | 节点侧代理：注册、心跳、执行脚本、内置装机引擎、终端会话 |
| `internal/panel` | 面板装配层（HTTP 路由、gRPC 服务端） |
| `internal/panel/agenthub` | agent 连接中枢：连接表、请求关联、会话路由、注册端点 |
| `internal/panel/store` | SQLite 持久化：节点、集群、成员、任务与日志 |
| `internal/panel/cluster` | 集群编排：首 master init → 接管 kubeconfig / join 凭据 → master/worker 加入 |
| `internal/panel/task` | 异步任务执行器：日志落库、取消、终态回调 |
| `internal/panel/kube` | client-go 直连 apiserver 的只读视图 |
| `internal/agent` | agent 运行时：呼出长连、串行执行队列、幂等、PTY 会话 |
| `internal/installer` | Kubernetes 装机引擎（自洽，只依赖标准库与系统命令） |
| `internal/pb/agentv1` | 由 `proto/` 生成的 Go 代码（入库） |
| `proto/agent/v1` | 面板 ↔ agent 契约 |
| `scripts/` | agent 安装脚本、离线包构建、发版 |
| `docs/` | 构建 / 部署 / 功能说明 |

## 快速开始（本地联调）

```bash
# 1. 起面板
go run ./cmd/starport-panel serve --bootstrap-token dev

# 2. 另一个终端起 agent（Linux 节点上跑真实装机；本机只验证链路）
go run ./cmd/starport-agent --server http://127.0.0.1:8080 --token dev --data-dir /tmp/sp-agent

# 3. 看节点、在节点上跑命令（异步任务，轮询日志）
curl -s http://127.0.0.1:8080/api/v1/nodes
curl -s -X POST http://127.0.0.1:8080/api/v1/nodes/1/exec -d '{"script":"uname -a"}'   # → {"taskId":1}
curl -s http://127.0.0.1:8080/api/v1/tasks/1/logs

# 4. 建集群：首 master 装机 → 控制面就绪 → 加 worker
curl -s -X POST http://127.0.0.1:8080/api/v1/clusters -d '{"name":"prod","artifactMode":"online","cni":"calico","vip":"10.0.0.100"}'
curl -s -X POST http://127.0.0.1:8080/api/v1/clusters/1/nodes -d '{"nodeId":1,"role":"first-master"}'   # → taskId
curl -s -X POST http://127.0.0.1:8080/api/v1/clusters/1/nodes -d '{"nodeId":2,"role":"worker"}'
curl -s http://127.0.0.1:8080/api/v1/clusters/1/kubeconfig
curl -s http://127.0.0.1:8080/api/v1/clusters/1/k8s/nodes
```

生产节点安装：`scripts/install-starport-agent.sh`（systemd 常驻，见 `docs/build.md`）。完整 API 见 [docs/api.md](docs/api.md)。

## 架构

```text
   浏览器 / API 调用方
          │  HTTP(S) :8080
   ┌──────▼──────────────────────────────┐
   │ starport-panel（控制面，一区一个）    │
   │  · API / UI  · 集群与应用编排        │
   │  · agenthub：连接表 + 请求关联        │
   └──────▲──────────────────────────────┘
          │  gRPC 双向流 :9192（agent 呼出）
   ┌──────┴──────┐  ┌─────────────┐  ┌─────────────┐
   │starport-agent│  │starport-agent│  │starport-agent│   每台节点一个
   │ 装机 / exec  │  │             │  │             │
   │ 终端会话     │  │             │  │             │
   └─────────────┘  └─────────────┘  └─────────────┘
```

agent 只做出站连接；面板沿同一条流反向下发 `exec` / `install` / `session_*`，agent 回 `log` / `result` / `session_data`。

## 路线图

- [x] Phase 0：仓库骨架、agent ↔ 面板 gRPC 链路、节点注册/在线/exec
- [x] Phase 1：SQLite 持久化、装集群编排（首 master → join，join 凭据自动刷新）、kubeconfig 接管、client-go 节点视图、异步任务与日志
- [ ] Phase 2：应用管理（部署 / 扩缩 / 暴露 / Helm）、Web 终端与日志（经 apiserver）、Web UI、集群删除/节点移除
- [ ] Phase 3：`panel/v1` 公开 API 定稿（gRPC + REST/OpenAPI）、API Token、多面板对接

## 开发

```bash
make build        # dist/starport-panel, dist/starport-agent
make agent-linux  # amd64 / arm64 静态 agent
make proto        # 改了 .proto 后重新生成（需 buf + protoc-gen-go + protoc-gen-go-grpc）
make test
```

约定见 [AGENTS.md](AGENTS.md)。

## 许可证

[PolyForm Noncommercial 1.0.0](LICENSE)：可自由使用、修改、分发于任何**非商业**目的；商业使用需另行取得授权。
