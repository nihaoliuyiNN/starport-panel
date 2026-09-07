# HTTP API（/api/v1）

响应一律 JSON；错误统一为 `{"error":{"code":"...","message":"..."}}`，HTTP 状态：400 参数错、404 不存在、409 状态冲突、502 apiserver 不可达、500 内部错误。
调用方按 `error.code` 分支，不解析 message。

> Phase 1 尚无鉴权；面板请只监听内网或置于反向代理之后。API Token 在 Phase 3 进入。

## 节点

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/agents/register` | agent 引导注册（Header `X-Starport-Bootstrap-Token`），非人用 |
| `GET` | `/nodes` | 全部节点：`facts`、`agentVersion`、`online`、`lastSeenAt` |
| `GET` | `/nodes/{id}` | 单节点 |
| `POST` | `/nodes/{id}/exec` | 在节点上执行脚本。体 `{"script":"...","timeoutMs":600000}` → `202 {"taskId":N}` |

## 集群

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/clusters` | 全部集群 |
| `POST` | `/clusters` | 建集群记录（尚无控制面）→ `201` 集群对象 |
| `GET` | `/clusters/{id}` | 集群 + `members[]`（每个成员：`nodeId`、`role`、`status`、`taskId`、`error`） |
| `POST` | `/clusters/{id}/nodes` | 把节点装进集群。体 `{"nodeId":N,"role":"first-master|join-master|worker"}` → `202 {"taskId":N}` |
| `GET` | `/clusters/{id}/kubeconfig` | admin kubeconfig（YAML）；控制面未就绪 `409 CLUSTER_NOT_READY` |
| `GET` | `/clusters/{id}/k8s/nodes` | 经 apiserver 列节点：`name`、`ready`、`roles`、`kubeletVersion`、`internalIp`、容量… |

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

集群状态：`created` → `installing` → `ready` | `failed`；成员状态：`installing` → `ready` | `failed`。

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
| `INVALID_ARGUMENT` | 参数不合法（message 说明哪个） |
| `NOT_FOUND` | 节点 / 集群 / 任务不存在 |
| `NODE_OFFLINE` | 节点 agent 未连接 |
| `NODE_ALREADY_MEMBER` | 节点已属于某集群 |
| `CLUSTER_NAME_EXISTS` | 集群名重复 |
| `CLUSTER_NOT_READY` | 控制面未就绪（不能加节点 / 取 kubeconfig） |
| `CLUSTER_HAS_CONTROL_PLANE` | 已有控制面，不能再 `first-master` |
| `NO_ONLINE_MASTER` / `JOIN_REFRESH_FAILED` | 刷新 join 凭据失败 |
| `TASK_NOT_RUNNING` | 取消已结束的任务 |
| `APISERVER_UNREACHABLE` | 面板连不上集群 apiserver |
