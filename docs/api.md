# HTTP API（/api/v1）

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
| `GET` | `/nodes` | 全部节点：`facts`、`agentVersion`、`online`、`lastSeenAt` |
| `GET` | `/nodes/{id}` | 单节点 |
| `POST` | `/nodes/{id}/exec` | 在节点上执行脚本。体 `{"script":"...","timeoutMs":600000}` → `202 {"taskId":N}` |
| `WS` | `/nodes/{id}/terminal?cols=120&rows=30&shell=&tty=true` | 节点 Web 终端（见下「终端协议」）；节点离线 `409 NODE_OFFLINE` |

## 集群

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/clusters` | 全部集群 |
| `POST` | `/clusters` | 建集群记录（尚无控制面）→ `201` 集群对象 |
| `GET` | `/clusters/{id}` | 集群 + `members[]`（每个成员：`nodeId`、`role`、`status`、`taskId`、`error`） |
| `DELETE` | `/clusters/{id}?force=false` | 删集群。仍有成员时 `409 CLUSTER_HAS_MEMBERS`；`force=true` 对每个在线成员发 `kubeadm reset` 任务并立即删记录 → `200 {"taskIds":[...]}` |
| `POST` | `/clusters/{id}/nodes` | 把节点装进集群。体 `{"nodeId":N,"role":"first-master|join-master|worker"}` → `202 {"taskId":N}` |
| `DELETE` | `/clusters/{id}/nodes/{nodeId}` | 移除成员：在另一台在线 master 上 `drain` + `delete node`，再在该节点 `kubeadm reset`，成功后删成员 → `202 {"taskId":N}` |
| `GET` | `/clusters/{id}/kubeconfig` | admin kubeconfig（YAML）；控制面未就绪 `409 CLUSTER_NOT_READY` |

移除成员规则：唯一的控制面不能单独移除（`409 LAST_MASTER`，请删集群）；成员 `installing|removing` 中 `409 MEMBER_BUSY`；
节点离线但有在线 master 时只做集群侧摘除、跳过本机 reset；失败的成员状态回到 `failed` 可重试。

## 集群内 Kubernetes 资源

面板用集群 admin kubeconfig 直连 apiserver；控制面未就绪一律 `409 CLUSTER_NOT_READY`。前缀 `/clusters/{id}/k8s`。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/nodes` | 节点：`name`、`ready`、`roles`、`kubeletVersion`、`internalIp`、容量… |
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
| `CLUSTER_NAME_EXISTS` | 集群名重复 |
| `CLUSTER_NOT_READY` | 控制面未就绪（不能加节点 / 取 kubeconfig） |
| `CLUSTER_HAS_CONTROL_PLANE` | 已有控制面，不能再 `first-master` |
| `CLUSTER_HAS_MEMBERS` | 删集群时仍有成员且未 `force` |
| `LAST_MASTER` | 唯一控制面不能单独移除 |
| `MEMBER_BUSY` | 成员正在装机 / 移除中 |
| `NO_ONLINE_MASTER` / `JOIN_REFRESH_FAILED` | 没有在线 master 可执行摘除 / 刷新 join 凭据失败 |
| `TASK_NOT_RUNNING` | 取消已结束的任务 |
| `APISERVER_UNREACHABLE` | 面板连不上集群 apiserver |
| `K8S_*` | apiserver 返回的状态错误（HTTP 码透传） |
| `EXEC_FAILED` | 终端 / 容器 exec 建流失败（出现在 WS exit 帧） |
