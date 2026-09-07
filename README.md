# starport-panel

自托管的 Kubernetes 面板：一个 Go 二进制起控制面，往裸机 / 物理机 / 云主机上装 `starport-agent`，
面板经 agent 把 Kubernetes（kubeadm HA + kube-vip + Cilium/Calico）装起来，再管理集群与应用。

- **单二进制、零依赖**：面板一个进程，节点侧一个静态二进制；没有 SSH、节点不开端口、天然穿 NAT。
- **离线优先**：装机制品（kube 二进制、containerd、CNI、镜像）打成离线包，节点无需外网；也支持在线模式 + 国内镜像源。
- **结构化装机**：面板下发的是结构化 `InstallSpec`，不是一坨脚本；预检 / 阶段 / 失败码都是数据，可重试、可断点。
- **契约先行**：面板 ↔ agent 走 gRPC 双向流，`.proto` 是唯一事实来源。

> 当前状态：**Phase 4 —— 功能闭环，待真机验证**。装机 / 接管集群、工作负载与任意资源、Helm 应用市场、实时用量、审计、TLS、备份、agent 自升级、OpenAPI 全部就位；尚未在真实多节点环境跑过端到端装机，见下方路线图。

## 组成

| 目录 | 内容 |
|---|---|
| `cmd/starport-panel` | 控制面进程：HTTP API（:8080）+ agent gRPC 入口（:9192） |
| `cmd/starport-agent` | 节点侧代理：注册、心跳、执行脚本、内置装机引擎、终端会话 |
| `internal/panel` | 面板装配层（HTTP 路由、鉴权、审计、TLS、gRPC 服务端、内嵌 `openapi.yaml`） |
| `internal/panel/ui` | 内嵌 Web UI 构建产物（`go:embed`），SPA 回退 |
| `web/` | Web UI 源码：React + Vite + Ant Design + xterm.js，`make ui` 构建进面板二进制 |
| `internal/panel/agenthub` | agent 连接中枢：连接表、请求关联、会话路由、注册端点、主动断连 |
| `internal/panel/store` | SQLite 持久化：节点、集群、成员、任务与日志、令牌、审计、Helm 仓库；`VACUUM INTO` 备份 |
| `internal/panel/cluster` | 集群编排：首 master init → 接管 kubeconfig / join 凭据 → master/worker 加入 → 节点移除 / 删集群；接管已有集群 |
| `internal/panel/task` | 异步任务执行器：日志落库、取消、终态回调 |
| `internal/panel/kube` | client-go 直连 apiserver：节点（cordon）/ 命名空间 / Pod / Deployment / Service / Ingress / 事件视图，扩缩、重启、暴露，日志流，容器 exec，server-side apply，任意 GVR 通用读写，metrics-server 用量 |
| `internal/panel/helm` | Helm Go SDK：仓库索引缓存、Chart 搜索 / 详情 / 版本，release 安装 / 升级 / 卸载 / 回滚 / 历史 |
| `internal/agent` | agent 运行时：呼出长连、串行执行队列、幂等、PTY 会话 |
| `internal/installer` | Kubernetes 装机引擎（自洽，只依赖标准库与系统命令） |
| `internal/pb/agentv1` | 由 `proto/` 生成的 Go 代码（入库） |
| `proto/agent/v1` | 面板 ↔ agent 契约 |
| `scripts/` | agent 安装脚本、离线包构建、发版 |
| `docs/` | 构建 / 部署 / 功能说明 |

## 快速开始（本地联调）

```bash
# 1. 起面板，签发一个 API 令牌（本机开发也可 --insecure-no-auth 跳过鉴权）
make ui                                                                           # 构建 Web UI（需 node + pnpm；跳过则只有 API）
go run ./cmd/starport-panel serve --bootstrap-token dev --data-dir /tmp/sp-panel
go run ./cmd/starport-panel token create --name dev --data-dir /tmp/sp-panel   # 打印 spt_...
export H='Authorization: Bearer spt_...'
# 浏览器打开 http://127.0.0.1:8080 ，用该令牌登录即可完成下面全部操作；以下是等价的 API 调用

# 2. 另一个终端起 agent（Linux 节点上跑真实装机；本机只验证链路）
go run ./cmd/starport-agent --server http://127.0.0.1:8080 --token dev --data-dir /tmp/sp-agent

# 3. 看节点、在节点上跑命令（异步任务，轮询日志）；下面所有 curl 都带 -H "$H"
curl -s -H "$H" http://127.0.0.1:8080/api/v1/nodes
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/nodes/1/exec -d '{"script":"uname -a"}'   # → {"taskId":1}
curl -s -H "$H" http://127.0.0.1:8080/api/v1/tasks/1/logs

# 4. 建集群：首 master 装机 → 控制面就绪 → 加 worker
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/clusters -d '{"name":"prod","artifactMode":"online","cni":"calico","vip":"10.0.0.100"}'
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/clusters/1/nodes -d '{"nodeId":1,"role":"first-master"}'   # → taskId
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/clusters/1/nodes -d '{"nodeId":2,"role":"worker"}'
curl -s -H "$H" http://127.0.0.1:8080/api/v1/clusters/1/kubeconfig
curl -s -H "$H" http://127.0.0.1:8080/api/v1/clusters/1/k8s/nodes

# 5. 管工作负载：apply YAML、看 Pod、拉日志、扩缩；节点 / 容器 Web 终端走 WebSocket
curl -s -H "$H" -X POST -H 'Content-Type: application/yaml' --data-binary @nginx.yaml http://127.0.0.1:8080/api/v1/clusters/1/k8s/apply
curl -s -H "$H" 'http://127.0.0.1:8080/api/v1/clusters/1/k8s/pods?namespace=default'
curl -s -H "$H" 'http://127.0.0.1:8080/api/v1/clusters/1/k8s/namespaces/default/pods/nginx-xxx/logs?tail=100'
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/clusters/1/k8s/namespaces/default/deployments/nginx/scale -d '{"replicas":3}'
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/clusters/1/k8s/namespaces/default/deployments/nginx/expose -d '{"type":"NodePort","port":80}'
curl -s -H "$H" 'http://127.0.0.1:8080/api/v1/clusters/1/k8s/events?namespace=default&object=Pod/nginx-xxx'
curl -s -H "$H" 'http://127.0.0.1:8080/api/v1/clusters/1/k8s/resources/core/v1/configmaps?namespace=kube-system'   # 任意 GVR
#   ws://127.0.0.1:8080/api/v1/nodes/1/terminal?token=spt_...        节点终端（xterm.js 直连）
#   ws://127.0.0.1:8080/api/v1/clusters/1/k8s/namespaces/default/pods/nginx-xxx/exec?token=spt_...   容器终端

# 6. Helm 应用：加仓库 → 搜 chart → 安装
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/clusters/1/helm/repos -d '{"name":"bitnami","url":"https://charts.bitnami.com/bitnami"}'
curl -s -H "$H" 'http://127.0.0.1:8080/api/v1/clusters/1/helm/charts?q=redis'
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/clusters/1/helm/releases -d '{"namespace":"cache","name":"redis","repo":"bitnami","chart":"redis","createNamespace":true,"values":"architecture: standalone\n"}'

# 7. 接管一个不是面板装的集群（只要 kubeconfig；之后 k8s / helm 接口全部可用，但不能加 / 移节点）
curl -s -H "$H" -X POST http://127.0.0.1:8080/api/v1/clusters/import -d "{\"name\":\"legacy\",\"kubeconfig\":$(jq -Rs . < ~/.kube/config)}"

# 8. 收尾：移除节点 / 删集群
curl -s -H "$H" -X DELETE http://127.0.0.1:8080/api/v1/clusters/1/nodes/2      # drain + delete node + kubeadm reset → taskId
curl -s -H "$H" -X DELETE 'http://127.0.0.1:8080/api/v1/clusters/1?force=true'  # 全部在线节点 reset 后删记录
```

生产部署：面板 `scripts/install-starport-panel.sh`、节点 `scripts/install-starport-agent.sh`（都是 systemd 常驻，见 [docs/build.md](docs/build.md)）。
完整 API 见 [docs/api.md](docs/api.md)；机器可读版 `GET /api/v1/openapi.yaml`。

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
- [x] Phase 2：节点移除 / 删集群、节点 Web 终端、工作负载视图（命名空间 / Pod / Deployment）、扩缩 / 重启 / 删 Pod、Pod 日志流、容器 exec、server-side apply
- [x] Phase 3a：API Token 鉴权（库内令牌 + 静态令牌，CLI 管理）、Service / Ingress / Events 视图、Deployment 暴露、通用资源接口（任意 GVR + CRD，YAML 查看）
- [x] Phase 3b：Web UI（React + Ant Design，随二进制内嵌）：节点 / 终端、建集群向导、成员管理、工作负载（Pod 日志 / exec / 扩缩 / 暴露）、网络、事件、YAML apply、任意资源浏览、任务日志、令牌管理
- [x] Phase 4：Helm 应用市场（仓库 / 搜索 / 安装 / 升级 / 回滚）、接管已有集群、实时用量（metrics-server）、节点 cordon、资源 YAML 在线编辑 apply、审计日志、面板 TLS、数据库备份、面板安装脚本、agent 自升级、machine-id 去重、OpenAPI 文档（随二进制提供并由测试校验与路由一致）
- [ ] Phase 5：真机端到端验证（多 master HA 装机、节点移除、离线包）；按验证结果修正装机引擎；OCI Helm 仓库；用量历史图表

## 开发

```bash
make build        # ui + dist/starport-panel（内嵌 UI）+ dist/starport-agent
make ui           # 只构建 Web UI → internal/panel/ui/dist
make ui-dev       # UI 开发服务器 :5173，/api 代理到 :8080 的面板
make agent-linux  # amd64 / arm64 静态 agent
make proto        # 改了 .proto 后重新生成（需 buf + protoc-gen-go + protoc-gen-go-grpc）
make test
```

约定见 [AGENTS.md](AGENTS.md)。

## 许可证

[PolyForm Noncommercial 1.0.0](LICENSE)：可自由使用、修改、分发于任何**非商业**目的；商业使用需另行取得授权。
