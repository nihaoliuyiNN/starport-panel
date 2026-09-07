# starport-agent 功能清单

驻在**裸机 / 物理机 / 云主机**上的轻量 Go 二进制，主动呼出连 starport-panel，
替代"控制面 SSH 进机器跑脚本"的装机方式。只当**本机执行器 + 呼出通道**，不做决策。

## 连接与安全

| 功能 | 说明 |
|---|---|
| 呼出而非被连 | 只对面板发起出站 gRPC 连接，机器上不监听端口、不保管 SSH 凭证，攻击面小、天然穿 NAT / 防火墙 |
| 反向通道 | gRPC 双向流长连（`NodeAgentService.Connect`），面板沿同一条流反向下发指令 |
| 多入口轮转 | 注册应答可下发多个 gRPC 入口，按重连次数轮转，单入口不可达不卡死 |
| 断线自愈 | 指数退避重连（1s→30s 封顶），稳定在线一分钟后重置退避，永不放弃 |
| 保活 | HTTP/2 keepalive（20s ping / 10s 超时）+ 应用层 `ping/pong` |
| TLS | 注册应答 `grpcTls=true` 或 `--grpc-tls` 时走系统根证书 TLS |
| 优雅退出 | 收到 SIGINT/SIGTERM 立即收链、停任务后退出 |

## 注册与身份

| 功能 | 说明 |
|---|---|
| 引导注册 | `POST /api/v1/agents/register` 用引导令牌换取 `nodeId`、长期 `agentToken`、心跳节奏与 gRPC 入口；瞬时故障重试，**令牌错误（401/403）直接退出**——密钥错则 agent 起不来，不空转伪装在装 |
| 身份持久化 | `nodeId + agentToken + grpcEndpoints` 落盘（`agent.json`，0600），重启复用不重复注册 |
| 令牌失效自恢复 | 长连返回 `UNAUTHENTICATED` → 清本地身份 → 重新注册 |

## 心跳与上报

| 功能 | 说明 |
|---|---|
| facts 采集 | 主机名、内网 IP、OS、架构、内核、CPU 核数、内存字节数、CPU/内存使用率 |
| 就绪首帧（ready） | 连接建立后立即上报版本 + facts |
| 周期心跳（heartbeat） | 按面板下发的节奏上报现状，供面板维护在线态 |

## 脚本执行

| 功能 | 说明 |
|---|---|
| 远程下发本机执行 | 面板下发 `exec{script,timeoutMs}`，agent 用 `/bin/bash`（`--shell` 可配）执行 |
| 实时日志流式回传 | 合并 stdout/stderr 逐行回传 `log` |
| 退出码与终态 | `result{ok, exitCode, error}` |
| 超时控制 | 按 `timeoutMs` 到点强杀（默认 30 分钟） |
| 可取消 | `cancel` 中止执行（Linux 下杀整个进程组，连带子进程） |
| 幂等去重 | 同 `requestId` 10 分钟内不重复执行，直接回放上次结果 |
| 串行执行 | 执行队列一次一个，避免同机并发脚本互相干扰（apt / kubeadm 抢锁） |
| 结构化错误码 | `EXEC_TIMEOUT` / `EXEC_FAILED` / `CANCELLED` / `SCRIPT_WRITE_FAILED` |

## 内置 K8s 装机（结构化指令，非脚本）

面板下发 `install{InstallSpec}`，agent 内置装机引擎（`internal/installer`）按角色编排本机装机阶段，
逐阶段流式回传日志，首 master 回传 join 凭据与 kubeconfig。

| 功能 | 说明 |
|---|---|
| 结构化下发 | `InstallSpec{role, k8sVersion, podCIDR, serviceCIDR, controlPlaneEndpoint, vip, cni, addons, join, artifact}` |
| 角色编排 | `first-master`（init --upload-certs）/ `join-master`（join --control-plane --certificate-key）/ `worker`（join） |
| 分阶段执行 | preflight → os 准备（swap/模块/sysctl）→ 制品就位 → containerd → 镜像导入 → kube 二进制 → [kube-vip] → kubeadm → [CNI] → [add-ons] |
| 幂等跳过 | 每阶段带探测（如 kubelet.conf 已存在则跳过 kubeadm），重跑安全 |
| 内置 kube-vip | 控制面 VIP 高可用，ARP/L2 静态 Pod，首 master init 前放置 |
| 离线包（bundle） | `artifact.mode=bundle`：下载（支持 `.parts.json` 分卷）→ 校验 → 解包 → `bin/`、`images/*.tar`（`ctr import`）、`cni/`、`addons/`、`systemd/`；节点无需外网 |
| 在线（online） | `artifact.mode=online`：apt 装 kube 组件 + containerd（可选国内镜像源），镜像经 registry mirror 拉取；CNI 清单内嵌于二进制 |
| CNI / add-on | 首 master apply Cilium/Calico；按 `addons` 逐个 `kubectl apply --server-side`（ingress-nginx / metrics-server / cert-manager） |
| 结果回传 | 首 master 回传 `result.install{joinCommand, token, caCertHash, certificateKey, kubeconfigB64}` |
| 可取消 | 装机无外部超时，支持 `cancel` 中止（杀整进程组） |
| 装机错误码 | `INSTALL_PREFLIGHT_FAILED` / `INSTALL_STEP_FAILED` / `INSTALL_UNSUPPORTED_OS` |

## 交互会话（Web 终端 / logs -f）

| 功能 | 说明 |
|---|---|
| 独立通道 | `session_*` 不走 exec 串行队列，各会话互不阻塞 |
| PTY | `tty=true` 时分配伪终端，支持 `session_resize` |
| 双向字节流 | `session_stdin` / `session_data` 原始字节，`session_exit` 终态 |

## 消息帧（见 `proto/agent/v1/agent.proto`）

| 方向 | 帧 | 含义 |
|---|---|---|
| 面板 → agent | `exec` / `install` / `cancel` / `ping` | 下发脚本 / 装机 / 取消 / 探活 |
| 面板 → agent | `session_open` / `session_stdin` / `session_resize` / `session_close` | 会话控制 |
| agent → 面板 | `ready` / `heartbeat` / `pong` | 就绪 / 心跳 / 探活应答 |
| agent → 面板 | `log` / `result` | 日志行 / 执行终态（装机随 `install` 带 InstallResult） |
| agent → 面板 | `session_data` / `session_exit` | 会话输出 / 会话结束 |

## 运行与配置

| 项 | 说明 |
|---|---|
| CLI 参数 | `--server`、`--token`、`--data-dir`（默认 `/var/lib/starport-agent`）、`--shell`、`--grpc`、`--grpc-tls`、`--version` |
| 环境变量 | 同名 `STARPORT_*` |
| 消息大小上限 | 单帧 4 MiB（容纳较大脚本） |
