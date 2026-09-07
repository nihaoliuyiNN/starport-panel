# HTTP API（/api/v1）

机器可读版：`internal/panel/openapi.yaml`（OpenAPI 3.1），面板运行时 `GET /api/v1/openapi.yaml` 也能取到；`go test ./internal/panel` 会校验它与路由表一致。本文是同一套接口的人读说明。

响应一律 JSON；错误统一为 `{"error":{"code":"...","message":"..."}}`，HTTP 状态：400 参数错、404 不存在、409 状态冲突、502 apiserver 不可达、500 内部错误；
apiserver 返回的状态错误按其原 HTTP 码透传，code 为 `K8S_<Reason>`（如 `K8S_NOTFOUND`、`K8S_FORBIDDEN`、`K8S_INVALID`）。
调用方按 `error.code` 分支，不解析 message。

## 鉴权

鉴权只覆盖 `/api/**`：除 agent 引导注册（`POST /api/v1/agents/register`，自有 bootstrap token）外，所有 API 请求须带令牌；`/healthz` 与 Web UI 静态资源（根路径及非 `/api` 路径，SPA 回退到 `index.html`）不鉴权。未匹配的 `/api/**` 路径返回 JSON 404 而不是页面。

- `Authorization: Bearer <token>`（首选）
- `?token=<token>`（仅供浏览器 WebSocket，无法自定义 Header 时使用）

缺失 → `401 UNAUTHENTICATED`。令牌来源两种，并行有效：

1. 库内令牌：`starport-panel token create --name ci`（面板运行中亦可执行）；明文 `spt_…` 只打印一次，库里只存 sha256。`token list` / `token revoke <id>` 管理。
2. 静态令牌：`serve --api-token <secret>`（或 `STARPORT_API_TOKEN`），适合自动化 / 上层系统按配置注入。

面板是单角色管理员模型：任一有效令牌拥有全部权限，包括管理令牌本身。本机开发可 `serve --insecure-no-auth` 关闭鉴权。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/tokens` | 全部令牌（含已吊销）：`id`、`name`、`prefix`、`createdAt`、`lastUsedAt`、`revokedAt` |
| `POST` | `/tokens` | 体 `{"name":"ci"}` → `201 {"token":{...},"plain":"spt_..."}`，`plain` 仅此一次 |
| `DELETE` | `/tokens/{id}` | 吊销（立即失效，幂等）→ `204` |

## 节点

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/agents/register` | agent 引导注册（Header `X-Starport-Bootstrap-Token`），非人用 |
| `GET` | `/agents/enroll` | 纳管新节点的材料：`{"bootstrapToken","version"}`，UI 据此拼一行安装命令 |
| `GET` | `/nodes` | 全部节点：`facts`、`agentVersion`、`online`、`lastSeenAt` |
| `GET` | `/nodes/{id}` | 单节点 |
| `DELETE` | `/nodes/{id}` | 删节点记录并断开连接（令牌随之失效）；仍是集群成员 `409 NODE_IN_CLUSTER`。机器上的 agent 服务需自行停掉，否则会重新注册成新节点 → `204` |
| `POST` | `/nodes/{id}/exec` | 在节点上执行脚本。体 `{"script":"...","timeoutMs":600000}` → `202 {"taskId":N}` |
| `WS` | `/nodes/{id}/terminal?cols=120&rows=30&shell=&tty=true` | 节点 Web 终端（见下「终端协议」）；节点离线 `409 NODE_OFFLINE` |
| `POST` | `/nodes/{id}/upgrade` | 升级该节点的 agent。体 `{"binUrl":"https://.../starport-agent","sha256":""}` → `202 {"taskId":N}`。节点下载 → 校验 → 试运行 `--version` → 覆盖二进制 → 3 秒后重启 systemd 服务并以新版本重连 |
| `POST` | `/nodes/upgrade` | 批量升级。体同上加 `nodeIds[]`（空 = 全部在线节点）→ `202 {"tasks":{nodeId:taskId},"skipped":[离线节点]}` |

节点去重：agent 上报 `facts.machineId`（`/etc/machine-id`）时按它识别机器，换 IP / 改主机名不会产生重复节点；没有 machine-id 的老 agent 退回 `hostname + internalIp`。

## 集群

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/clusters` | 全部集群（`source` 为 `kubeadm` 面板装机 / `imported` 接管） |
| `POST` | `/clusters` | 建集群记录（尚无控制面）→ `201` 集群对象 |
| `POST` | `/clusters/probe` | 体 `{"kubeconfig":"..."}`，只直连探测不入库 → `{"version","endpoint","nodeCount","podCIDR"}`；连不上 `400 KUBECONFIG_UNREACHABLE` |
| `POST` | `/clusters/import` | 接管已有集群。体 `{"name":"legacy","kubeconfig":"..."}`，探测成功才入库（`source=imported`、`status=ready`）→ `201 {"cluster":{...},"probe":{...}}` |
| `GET` | `/clusters/{id}` | 集群 + `members[]`（每个成员：`nodeId`、`role`、`status`、`taskId`、`error`） |
| `DELETE` | `/clusters/{id}?force=false` | 删集群。仍有成员时 `409 CLUSTER_HAS_MEMBERS`；`force=true` 对每个在线成员发 `kubeadm reset` 任务并立即删记录 → `200 {"taskIds":[...]}`。接管的集群只删记录 |
| `POST` | `/clusters/{id}/nodes` | 把节点装进集群。体 `{"nodeId":N,"role":"first-master|join-master|worker"}` → `202 {"taskId":N}`；接管的集群 `409 CLUSTER_IMPORTED` |
| `DELETE` | `/clusters/{id}/nodes/{nodeId}` | 移除成员：在另一台在线 master 上 `drain` + `delete node`，再在该节点 `kubeadm reset`，成功后删成员 → `202 {"taskId":N}` |
| `GET` | `/clusters/{id}/kubeconfig` | admin kubeconfig（YAML）；控制面未就绪 `409 CLUSTER_NOT_READY` |
| `PUT` | `/clusters/{id}/kubeconfig` | 替换接管集群的 kubeconfig（证书轮换后）。体 `{"kubeconfig":"..."}`，保存前探测一次；非接管集群 `409 CLUSTER_NOT_IMPORTED` → `204` |

接管的集群面板没有它的 kubeadm 凭据，只做集群内资源管理（下文 `k8s` / `helm` 全部可用），不能经面板加 / 移节点。

移除成员规则：唯一的控制面不能单独移除（`409 LAST_MASTER`，请删集群）；成员 `installing|removing` 中 `409 MEMBER_BUSY`；
节点离线但有在线 master 时只做集群侧摘除、跳过本机 reset；失败的成员状态回到 `failed` 可重试。

## 集群内 Kubernetes 资源

面板用集群 admin kubeconfig 直连 apiserver；控制面未就绪一律 `409 CLUSTER_NOT_READY`。前缀 `/clusters/{id}/k8s`。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/nodes` | 节点：`name`、`ready`、`unschedulable`、`roles`、`kubeletVersion`、`internalIp`、容量… |
| `POST` | `/nodes/{name}/cordon` · `/uncordon` | 停止 / 恢复调度（不驱逐已有 Pod）→ `204` |
| `GET` | `/metrics/nodes` | 节点实时用量（需 metrics-server，否则 `404 NO_METRICS_SERVER`）：`cpuMilli`、`cpuCapMilli`、`memBytes`、`memCapBytes`、`cpuPercent`、`memPercent` |
| `GET` | `/metrics/pods?namespace=` | Pod 实时用量：`cpuMilli`、`memBytes`、`containers[]{name,cpuMilli,memBytes}` |
| `GET` | `/namespaces` | 命名空间：`name`、`status`、`createdAt` |
| `GET` | `/pods?namespace=` | Pod（空 namespace 为全部）：`phase`、`ready`（如 `1/2`）、`restarts`、`node`、`podIp`、`containers[]{name,image,ready,state}` |
| `DELETE` | `/namespaces/{ns}/pods/{name}` | 删 Pod（由控制器重建）→ `204` |
| `GET` | `/namespaces/{ns}/pods/{name}/logs?container=&tail=500&previous=false&follow=false` | 容器日志 `text/plain`；`follow=true` 分块流式，直到断开或容器结束 |
| `WS` | `/namespaces/{ns}/pods/{name}/exec?container=&cmd=&cols=&rows=` | 容器 Web 终端（协议同节点终端）。`cmd` 可重复给 argv，默认 `bash` 不存在则回落 `sh` |
| `GET` | `/deployments?namespace=` | Deployment：`replicas`、`ready`、`updated`、`available`、`images[]`、`labels` |
| `POST` | `/namespaces/{ns}/deployments/{name}/scale` | 体 `{"replicas":3}` → `204` |
| `POST` | `/namespaces/{ns}/deployments/{name}/restart` | 滚动重启（同 `kubectl rollout restart`）→ `204` |
| `POST` | `/namespaces/{ns}/deployments/{name}/expose` | 为 Deployment 建 Service（同名已存在则更新）。体 `{"name":"","type":"ClusterIP|NodePort|LoadBalancer","port":80,"targetPort":8080,"nodePort":0,"protocol":"TCP"}` → `201` Service |
| `GET` | `/services?namespace=` | Service：`type`、`clusterIp`、`externalIps[]`（含 LB 回填）、`ports[]{port,targetPort,nodePort,protocol}`、`selector` |
| `DELETE` | `/namespaces/{ns}/services/{name}` | 删 Service → `204` |
| `GET` | `/ingresses?namespace=` | Ingress：`class`、`rules[]{host,path,service,port}`、`tlsHosts[]`、`addresses[]` |
| `GET` | `/events?namespace=&object=Pod/nginx-abc` | 事件（最近发生倒序）：`type`、`reason`、`message`、`object`、`count`、`firstSeen`、`lastSeen`；`object` 形如 `Kind/name` 只看该对象 |
| `POST` | `/apply?namespace=default` | server-side apply 多文档 YAML（`kubectl apply --server-side --force-conflicts`）→ `{"applied":[{kind,namespace,name}]}` |
| `POST` | `/delete?namespace=default` | 按 YAML 删对象（`kubectl delete -f`，不存在忽略）→ `{"deleted":[...]}` |

`apply` / `delete` 正文两种给法：`Content-Type: application/yaml` 直接放 YAML；或 JSON `{"manifest":"...","namespace":"default"}`。
无 `metadata.namespace` 的 namespaced 资源落到 `namespace` 参数（默认 `default`）。任一文档失败即停止，响应同时带已成功的 `applied` 与 `error`，已成功的不回滚。

### 通用资源（任意 GVR，含 CRD）

精简视图覆盖不到的资源走通用接口。路径 `{group}/{version}/{resource}`，核心组用 `core`（如 `core/v1/configmaps`、`apps/v1/statefulsets`、`cert-manager.io/v1/certificates`）。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/resource-kinds` | 集群支持的全部可 list 资源类型：`group`、`version`、`resource`、`kind`、`namespaced`、`verbs[]` |
| `GET` | `/resources/{group}/{version}/{resource}?namespace=&labelSelector=` | 列表（只带元数据）：`namespace`、`name`、`labels`、`createdAt` |
| `GET` | `/resources/{group}/{version}/{resource}/{name}?namespace=` | 单对象完整 YAML（`application/yaml`，去掉 `managedFields`）；集群级资源省略 `namespace` |
| `DELETE` | `/resources/{group}/{version}/{resource}/{name}?namespace=` | 删对象 → `204` |

## Helm 应用

前缀 `/clusters/{id}/helm`。仓库按集群配置（不同集群可用不同源），release 直接经 kubeconfig 操作（Helm Go SDK，无需节点上装 helm）。
暂只支持 http(s) 仓库（`oci://` → `400 OCI_UNSUPPORTED`）；索引缓存 10 分钟。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/repos` | 仓库：`name`、`url`、`hasAuth`、`createdAt` |
| `POST` | `/repos` | 体 `{"name":"bitnami","url":"https://...","username":"","password":""}`，会拉一次 index.yaml 验证 → `201`；重名 `409 REPO_NAME_EXISTS`，拉不到 `400 REPO_UNREACHABLE` |
| `DELETE` | `/repos/{repo}` | 删仓库（不影响已部署 release）→ `204` |
| `POST` | `/repos/{repo}/refresh` | 强制刷新索引 → `204` |
| `GET` | `/charts?q=&repo=` | 搜索（名称 / 描述 / 关键词子串；每个 chart 只给最新版）→ `{"charts":[...],"errors":{repo:原因}}`，个别仓库拉取失败不影响其它 |
| `GET` | `/charts/{repo}/{chart}?version=` | 详情：基本信息 + `readme` + 默认 `values`（保留注释）；不存在 `404 CHART_NOT_FOUND` |
| `GET` | `/charts/{repo}/{chart}/versions` | 全部版本（新 → 旧）：`version`、`appVersion`、`created` |
| `GET` | `/releases?namespace=` | release：`name`、`namespace`、`revision`、`status`、`chart`、`chartName`、`chartVersion`、`appVersion`、`updated`、`notes` |
| `POST` | `/releases` | 安装（同步）。体 `{"namespace","name","repo","chart","version":"","values":"yaml 文本","createNamespace":true,"wait":false,"timeoutSeconds":300}` → `201` release |
| `PUT` | `/releases/{ns}/{name}` | 升级。体同上（`namespace`/`name` 取路径）；`values` 整体替换，不与旧值合并 → `200` release |
| `DELETE` | `/releases/{ns}/{name}` | 卸载 → `204`；不存在 `404 RELEASE_NOT_FOUND` |
| `POST` | `/releases/{ns}/{name}/rollback` | 体 `{"revision":N}` → `204` |
| `GET` | `/releases/{ns}/{name}/history` | 修订历史（新 → 旧），结构同 release |
| `GET` | `/releases/{ns}/{name}/values` | 当前用户 values → `{"values":"yaml 文本"}` |

Helm / apiserver 侧的失败（release 已存在、模板渲染失败、`wait` 超时等）统一 `502 HELM_ERROR`，message 为 Helm 原始错误。

## 审计

所有 `POST / PUT / PATCH / DELETE` 的 `/api/**` 调用（通过鉴权后）都会落一条审计记录：调用者令牌、方法、路径、响应状态、来源 IP（认 `X-Forwarded-For` 首段）、耗时。不存请求体。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/audit?before=&limit=100` | 倒序；用最后一条 `id` 作 `before` 翻页。字段：`id`、`at`、`tokenId`、`tokenName`（静态令牌 `static`、关闭鉴权 `anonymous`）、`method`、`path`、`status`、`remoteIp`、`durationMs` |

## 终端协议（WebSocket）

节点终端与容器 exec 共用同一协议，浏览器端用 xterm.js 可直接对接：

- **二进制帧**：终端字节流，双向（浏览器键入 → 服务端；进程输出 → 浏览器）。
- **文本帧（浏览器 → 服务端）**：控制消息 `{"type":"resize","cols":N,"rows":N}`。
- **文本帧（服务端 → 浏览器）**：会话结束 `{"type":"exit","exitCode":N,"error":{code,message}|null}`，随后正常关闭。

节点终端参数：`cols`/`rows` 初始窗口（默认 120×30）；`shell` 为 `sh -c` 执行的脚本（默认 `exec bash -l`）；`tty=false` 关闭伪终端做纯管道。
节点 PTY 仅 Linux agent 支持。

建集群体（全部可省，取默认）：

```json
{
  "name": "prod",
  "k8sVersion": "v1.35.7",
  "podCIDR": "10.244.0.0/16",
  "serviceCIDR": "10.96.0.0/12",
  "controlPlaneEndpoint": "10.0.0.100:6443",
  "vip": "10.0.0.100",
  "vipInterface": "",
  "cni": "cilium",
  "cniVersion": "1.16.5",
  "addons": ["ingress-nginx", "metrics-server", "cert-manager"],
  "artifactMode": "bundle",
  "bundleUrl": "https://.../k8s-bundle-v1.35.7-amd64-cilium.tar.gz",
  "useCnMirror": true
}
```

规则：

- `artifactMode=bundle` 必须给 `bundleUrl`；`online` 只支持 `cni=calico`。
- `controlPlaneEndpoint` 空时：有 `vip` 用 `vip:6443`，否则在首 master 装机时用其内网 IP:6443 回填。
- 加节点顺序：先一台 `first-master`（集群 `created|failed` → `installing` → `ready`），之后才能 `join-master` / `worker`。
- 一台机只能属于一个集群；同集群上次 `failed` 的成员允许重试。
- join 凭据（token 24h / certificate-key 2h）过期时，面板自动在一台在线 master 上重新签发再下发。

集群状态：`created` → `installing` → `ready` | `failed`；成员状态：`installing` → `ready` | `failed`，移除中为 `removing`。

## 任务

装机与脚本执行都是异步任务：日志逐行落库，用 `after` 增量拉取。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/tasks?nodeId=&limit=` | 最近任务（倒序） |
| `GET` | `/tasks/{id}` | 任务：`kind`、`status`、`exitCode`、`errorCode`、`errorMessage` |
| `GET` | `/tasks/{id}/logs?after=0&limit=1000` | `{"task":{...},"logs":[{"seq","at","line"}]}`；用最后一条 `seq` 续拉，`task.status != running` 即结束 |
| `POST` | `/tasks/{id}/cancel` | 取消运行中任务（向 agent 发 cancel）→ `204`；已结束 `409 TASK_NOT_RUNNING` |

任务状态：`running` → `succeeded` | `failed` | `cancelled`。面板重启时遗留的 `running` 任务被标为 `failed / PANEL_RESTARTED`。

## 常见错误码

| code | 含义 |
|---|---|
| `UNAUTHENTICATED` | 缺少 / 无效 / 已吊销的 API 令牌 |
| `INVALID_ARGUMENT` | 参数不合法（message 说明哪个） |
| `NOT_FOUND` | 节点 / 集群 / 任务不存在 |
| `NODE_OFFLINE` | 节点 agent 未连接 |
| `NODE_ALREADY_MEMBER` | 节点已属于某集群 |
| `NODE_IN_CLUSTER` | 删节点记录时它仍是集群成员 |
| `CLUSTER_NAME_EXISTS` | 集群名重复 |
| `CLUSTER_NOT_READY` | 控制面未就绪（不能加节点 / 取 kubeconfig） |
| `CLUSTER_IMPORTED` / `CLUSTER_NOT_IMPORTED` | 接管集群不能加节点 / 非接管集群不能替换 kubeconfig |
| `KUBECONFIG_UNREACHABLE` | 探测 / 接管时用给定 kubeconfig 连不上 apiserver |
| `CLUSTER_HAS_CONTROL_PLANE` | 已有控制面，不能再 `first-master` |
| `CLUSTER_HAS_MEMBERS` | 删集群时仍有成员且未 `force` |
| `LAST_MASTER` | 唯一控制面不能单独移除 |
| `MEMBER_BUSY` | 成员正在装机 / 移除中 |
| `NO_ONLINE_MASTER` / `JOIN_REFRESH_FAILED` | 没有在线 master 可执行摘除 / 刷新 join 凭据失败 |
| `TASK_NOT_RUNNING` | 取消已结束的任务 |
| `APISERVER_UNREACHABLE` | 面板连不上集群 apiserver |
| `NO_METRICS_SERVER` | 集群未装 metrics-server，用量接口不可用 |
| `K8S_*` | apiserver 返回的状态错误（HTTP 码透传） |
| `REPO_NAME_EXISTS` / `REPO_UNREACHABLE` / `OCI_UNSUPPORTED` | Helm 仓库重名 / 索引拉不到 / 暂不支持 OCI |
| `CHART_NOT_FOUND` / `RELEASE_NOT_FOUND` / `HELM_ERROR` | Chart 不在任何已配置仓库 / release 不存在 / Helm 操作失败（502，message 为原始错误） |
| `EXEC_FAILED` | 终端 / 容器 exec 建流失败（出现在 WS exit 帧） |
