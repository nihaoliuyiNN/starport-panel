# AGENTS — starport-panel

自托管 Kubernetes 面板（开源，PolyForm Noncommercial）。Go 单仓库，两个二进制。

## 定位与边界

- **本仓库是"大脑"**：节点纳管、装机编排、集群/应用管理、终端/日志、对外 API，全部在这里、全部用 Go。
- 商业层（多租户、计费、多可用区调度）是另一个闭源仓库（Java），**只经本面板的公开 API 调用**，不直连节点、不碰 kubectl。
  因此本仓库不得出现任何商业层专有概念（租户、账单、可用区…），API 必须自洽、对任何调用方一视同仁。
- 面板一区一个、有状态；agent 每节点一个、无状态（身份落盘）。

## 模块

- `cmd/starport-panel` — 控制面进程，子命令 `serve` / `token` / `backup` / `version`。
- `cmd/starport-agent` — 节点侧代理（Linux 生产；其它平台仅可编译）。
- `internal/panel` — 装配层：`server.go` 拼装子系统（含 TLS），`api.go` HTTP 路由与统一错误映射，`api_*.go` 按领域拆处理器（nodes / kube / helm），`audit.go` 写操作审计中间件。业务不写在这里。
- `internal/panel/openapi.yaml` — 公开 API 的 OpenAPI 3.1 描述，`go:embed` 后由 `GET /api/v1/openapi.yaml` 提供。**加 / 改路由必须同步改它**：`openapi_test.go` 会比对 `api.go` 里注册的每条路由与文档路径 + 方法，漂移即测试失败。`docs/api.md` 是同一内容的人读版，也一并更新。
- `internal/panel/helm` — Helm Go SDK 封装：仓库索引缓存（按 URL+用户名，10 分钟）、Chart 搜索 / 详情 / 版本、release 安装 / 升级 / 卸载 / 回滚 / 历史 / values。用 `restGetter` 让 SDK 直接吃 kubeconfig 字符串，不落盘、不依赖节点上的 helm。只支持 http(s) 仓库，OCI 返回 `ErrOCIUnsupported`。
- `internal/panel/agenthub` — agent 连接中枢。面板任何"对节点做事"都经 `Hub.Exec / Install / OpenSession`，不得绕过。
- `internal/panel/store` — SQLite 持久化（modernc，cgo-free）。**所有 SQL 只在此包**；schema 在 `schema.sql`，改结构走幂等 `IF NOT EXISTS` / 新增列迁移。
- `internal/panel/task` — 异步任务执行器：`Runner.Start(kind, nodeID, clusterID, fn, onDone)`，日志逐行落库，终态回调推进上层状态。
- `internal/panel/cluster` — 集群编排（建集群 / 加节点 / 接管凭据 / join 刷新 / 移除节点 / 删集群 / 接管已有集群）。依赖 `cluster.Hub` 接口而非具体 hub，便于测试；删集群后经 `OnDeleted` 通知 kube 丢缓存。集群 `Source` 区分 `kubeadm`（面板装的，可加 / 移节点）与 `imported`（只有 kubeconfig，只做资源管理）——凡是需要 kubeadm 凭据的操作先查 `Source`。
- `internal/panel/kube` — client-go 直连 apiserver：`kube.go` 客户端缓存（按 kubeconfig 哈希，clientset + dynamic + discovery + metrics）与 `Probe`（接管前探测）/ `SetUnschedulable`，`workloads.go` 列表与扩缩 / 重启 / 删 Pod / 日志流，`exec.go` 容器 exec（WebSocket 优先回落 SPDY），`apply.go` server-side apply / delete，`metrics.go` metrics-server 用量（未安装返回 `ErrNoMetricsServer` → 404，UI 据此隐藏用量列）。只有面板 import client-go，agent 二进制不受影响。
- 节点身份：agent 上报 `Facts.MachineID`（`/etc/machine-id`），`store.RegisterNode` 优先按它去重，退回 `hostname+internalIp` 只匹配尚无 machine_id 的记录。删节点记录（`DELETE /nodes/{id}`）须先确认不是集群成员，随后 `hub.Kick` 断连。
- `internal/panel/terminal.go` / `api_kube.go` — WebSocket 桥接（节点 PTY、容器 exec）与 k8s 资源 HTTP 处理器。终端协议：二进制帧=字节流，文本帧=控制 JSON（`resize` / `exit`），两处一致，不要各造一套。
- `internal/panel/ui` — `go:embed all:dist` 内嵌 Web UI；`dist/` 是构建产物（只提交 `.gitkeep`），`make ui` 生成。未构建时根路径返回 503 提示，API 不受影响。
- `web/` — UI 源码（React 18 + Vite + Ant Design 5 + TanStack Query + xterm.js，pnpm）。约定：`src/api/` 是唯一的 HTTP 层（`types.ts` 与 Go JSON 一一对应，改后端结构必须同步）；页面用 antd 原生 `Table`，不引 pro-components；所有表格 `scroll={{ x: 'max-content' }}`；WebSocket 经 `wsUrl()` 带 `?token=`；路由组件 `lazy()` 按页拆包。资源 YAML 查看 / 编辑统一用 `components/YamlDrawer`（读通用资源接口 → 编辑 → apply），不要再写第二个 YAML 抽屉。UI 只消费公开 API，不得出现任何 UI 专用后端接口。
- `internal/panel/auth.go` — API 令牌中间件（Bearer / `?token=`），只保护 `/api/**`，静态资源放行。库内令牌 `store/tokens.go` 只存 sha256；静态令牌 `Config.APIToken`。通过后把 `Actor` 放进 ctx 供 `audit.go` 记录。单角色管理员模型，暂无细粒度权限——需要多租户 / RBAC 的是商业层的事，不要加进来。
- `internal/panel/api_nodes.go` — agent 自升级：面板下发 shell 脚本（下载 → sha256 → `--version` 试运行 → 覆盖二进制 → `systemd-run` 延迟重启），任务先得终态再重启。改脚本时保持"脚本正常返回后才重启"这一约束，否则任务永远拿不到结果。
- `kube/network.go`（Service / Ingress / Events / expose）、`kube/resources.go`（任意 GVR 的通用 list / get YAML / delete，供 UI 兜底）。新增"精简视图"只在高频资源上做；长尾资源一律走通用接口，不要为每种 Kind 再写一套结构体。
- `internal/agent` — agent 运行时（`conn.go` 流、`exec.go` 串行执行、`stream.go` PTY 会话、`state.go` 身份）。
- `internal/installer` — 装机引擎。**只依赖标准库与系统命令**，不 import 本仓库其它包（agent 以类型别名复用其类型）。
- `internal/pb/agentv1` — 生成代码，**禁止手改**；改 `proto/` 后 `make proto`。
- `proto/agent/v1/agent.proto` — 面板 ↔ agent 契约，唯一事实来源。

## 协议约定

- 面板 ↔ agent：gRPC 双向流（`NodeAgentService.Connect`），agent 呼出、面板反向下发。metadata 键 `x-starport-agent-token` 等见 `internal/agent/proto.go`。
- 引导注册：HTTP `POST /api/v1/agents/register`（`agent.RegisterPath`），应答含 `grpcEndpoints`。
- 改 proto：只追加字段编号，不复用、不改；`buf lint` + `buf breaking --against '.git#subdir=proto'` 必须过。
- 错误一律结构化 `Error{code,message,retryable}`，调用方按 `code` 分支，不解析字符串。

## Go 约定

- `go 1.25`，`gofmt`/`go vet` 零告警；错误用 `errors.Is/As`，包级 sentinel 放包顶部。
- 注释与日志中文；日志前缀 `[panel]` / `[agenthub]` / `[agent]`。
- 并发：gRPC 流 `Send` 不可并发——每条连接一个 writer goroutine + channel 汇聚，不要在别处直接 `Send`。
- 面板 HTTP 用标准库 `net/http` `ServeMux`（Go 1.22+ 方法/路径模式），不引路由框架。
- 存储：SQLite 单文件（`store` 包），保持单二进制；子系统对存储的依赖用小接口声明在消费方（如 `agenthub.Store`）。
- HTTP 错误：业务错误用 `cluster.Error{Code, Message, Status}`，`api.go` 的 `writeErr` 统一映射；`store.ErrNotFound` → 404。
- 不为"以后可能用到"加抽象；不做的功能不留空壳。

## 常用命令

- 构建：`make build`（含 UI）；只构建 UI：`make ui`；UI 开发：`make ui-dev`（:5173 代理 /api → :8080）；agent 静态交叉编译：`make agent-linux`。
- 协议：`make proto`（需 `buf`、`protoc-gen-go`、`protoc-gen-go-grpc` 在 PATH）。
- 本地联调：`go run ./cmd/starport-panel serve --bootstrap-token dev` + `go run ./cmd/starport-agent --server http://127.0.0.1:8080 --token dev --data-dir /tmp/sp`。
- 离线装机包：`scripts/build-k8s-bundle.sh`（产物不入库）。
