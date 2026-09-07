# 构建与部署

starport-panel 仓库有三类产物：

1. **starport-panel 二进制**（`cmd/starport-panel`）— 控制面，一个可用区 / 一套集群部署一个。
2. **starport-agent 二进制**（`cmd/starport-agent`）— 装到每台节点，呼出连面板。
3. **K8s 离线包** `k8s-bundle.tar.gz`（`scripts/build-k8s-bundle.sh`）— agent 内置装机用（在线模式不需要）。

前置：Go 1.25+（`go.mod` 模块名 `starport-panel`）。命令均在仓库根目录执行；有 `make` 的环境直接用 `Makefile`。

---

## 1. 面板 starport-panel

```bash
make panel                                   # dist/starport-panel
./dist/starport-panel serve --bootstrap-token <随机长字符串>
```

参数（亦可用环境变量）：

| 参数 | 环境变量 | 默认 | 说明 |
|---|---|---|---|
| `--http` | `STARPORT_HTTP_ADDR` | `:8080` | API / UI / agent 注册 |
| `--grpc` | `STARPORT_GRPC_ADDR` | `:9192` | agent 呼出长连入口 |
| `--data-dir` | `STARPORT_DATA_DIR` | Linux `/var/lib/starport-panel`，其它 `./data` | 状态目录：SQLite `panel.db`（WAL） |
| `--bootstrap-token` | `STARPORT_BOOTSTRAP_TOKEN` | 必填 | agent 引导注册令牌 |
| `--grpc-endpoints` | `STARPORT_GRPC_ENDPOINTS` | 按注册请求的主机推导 | 下发给 agent 的 gRPC 入口（面板在 LB/NAT 后时显式指定） |

两个端口都要对节点可达：8080 用于注册，9192 用于长连。面板还需能访问集群 apiserver（`controlPlaneEndpoint`）以提供 `k8s/nodes` 视图。

状态全部在 `panel.db` 一个文件里（节点凭据、集群 kubeconfig / join 凭据、任务日志），备份即拷贝该文件（WAL 模式下连同 `-wal` 一起）。API 见 [api.md](api.md)。

---

## 2. 节点代理 starport-agent

agent 只跑在 Linux，需交叉编译；`version` 用 `-ldflags` 注入（不注入默认 `dev`）。

```bash
make agent-linux        # dist/starport-agent-linux-{amd64,arm64}，CGO_ENABLED=0 静态
```

**Windows PowerShell**（不能写 `GOOS=linux go build`）：

```powershell
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -ldflags "-s -w -X main.version=v1.0.1" -o dist/starport-agent ./cmd/starport-agent
```

### 一条命令发版（Gitee Release）

`scripts/publish-agent-release.ps1` 把交叉编译、建 Release、传附件、校验直链一起做完：

```powershell
cd scripts
.\publish-agent-release.ps1 -Version v1.0.9              # 编译 + 建 Release + 传附件 + 校验
.\publish-agent-release.ps1 -Version v1.0.9 -SkipBuild   # 二进制已编好，只补传附件
.\publish-agent-release.ps1 -Version v1.0.9 -Replace     # 重发同版本，覆盖同名附件
.\publish-agent-release.ps1 -Version v1.0.9 -Arch arm64
```

令牌从 `E:\Develop\.gitee-token` 读（Gitee「设置 → 安全设置 → 私人令牌」，勾 `projects`）。
`install-starport-agent.sh` 会被转成 LF 再传，根治下面那个 `$'\r'` 报错。
最后一步从**公开直链**下回来比 sha256——安装脚本用的就是那个 URL。

### 节点首次安装（需 root）

`install-starport-agent.sh` 下载二进制到 `/usr/local/bin/starport-agent`、写 systemd 单元并 `enable --now`：

```bash
curl -fsSL https://gitee.com/nihaoliuyi/starport-agent/releases/download/<tag>/install-starport-agent.sh | \
  STARPORT_SERVER_URL=http://panel.example.com:8080 \
  STARPORT_BOOTSTRAP_TOKEN=<面板的 --bootstrap-token> \
  STARPORT_AGENT_BIN_URL=https://gitee.com/nihaoliuyi/starport-agent/releases/download/<tag>/starport-agent \
  bash
```

- `STARPORT_SERVER_URL`：面板 HTTP 地址。
- `STARPORT_BOOTSTRAP_TOKEN`：须与面板 `--bootstrap-token` 一致。
- `STARPORT_AGENT_BIN_URL`：starport-agent 二进制直链。

> 报 `: invalid option nameipefail` 或 `$'\r': command not found`，是脚本被存成了 CRLF。
> 临时绕过：`curl -fsSL <脚本URL> | sed 's/\r$//' | STARPORT_...=... bash`。

装完验证：

```bash
starport-agent --version
journalctl -u starport-agent -f   # 应看到「注册成功」「已连接控制面 gRPC」
curl -s http://panel.example.com:8080/api/v1/nodes   # 面板侧应出现该节点 online=true
```

### 日常运维（systemd）

```bash
systemctl status  starport-agent
systemctl restart starport-agent   # 改了 --server/--token 或换二进制后
journalctl -u starport-agent -f
```

**更新二进制**：先下临时文件再 `mv` 原子替换，再重启。不要直接 `curl -o /usr/local/bin/starport-agent`
——会 truncate 正在运行的可执行文件（`ETXTBSY`）。

```bash
curl -fsSL <新版直链> -o /tmp/starport-agent && chmod +x /tmp/starport-agent
mv -f /tmp/starport-agent /usr/local/bin/starport-agent
systemctl restart starport-agent
```

也可由面板下发脚本远程完成（下载 → `--version` 校验 → 原子替换 → `systemd-run` 瞬态定时器重启，
脱离 agent 自身 cgroup，避免重启把执行中的脚本一起杀掉）。

> 令牌错误会 fail-fast 退出；`systemctl status` 显示 `activating (auto-restart)` 且日志反复报
> 「引导令牌无效」，说明 `--token` 与面板 `--bootstrap-token` 不一致。

---

## 3. 装机制品来源：在线 vs 离线包

创建集群（首个 Master）时选「制品来源」：

| 模式 | 节点要求 | 要不要构建/托管大包 | CNI | 适用 |
|---|---|---|---|---|
| **在线 online** | 节点有外网（NAT） | **不用** | 仅 calico | 云主机带公网/NAT，图省事 |
| **离线包 bundle** | 节点无需外网 | 要（见 §3.2） | cilium / calico | 内网 / 隔离环境 |

### 3.1 在线模式

节点侧全部在线拉取，面板只下发结构化 InstallSpec：

- 二进制：`apt` 装 kubeadm/kubelet/kubectl + containerd（`useCnMirror` 走 `mirrors.aliyun.com`，否则官方 `pkgs.k8s.io`）。
- 控制面镜像：kubeadm `imageRepository = registry.aliyuncs.com/google_containers`。
- 其余镜像（calico/addon 等按 digest 钉死的）：containerd 配 `registry mirror`（daocloud）透传拉取。
- pause：sandbox_image 指向阿里云 pause。
- CNI / add-on 清单：**已内嵌进 agent 二进制**（`internal/installer/manifests/`），装机时落盘 `kubectl apply`。

**前提**：节点能访问外网。CNI 固定 **calico**（cilium 需 helm，在线模式不支持）。

升级内嵌清单：改 `manifests/` 下对应 YAML + `installer/embed.go` 顶部版本注释，重编 agent
（默认与 `build-k8s-bundle.sh` 对齐：calico v3.29.1 / ingress-nginx controller-v1.11.3 / metrics-server v0.7.2 / cert-manager v1.16.2）。

### 3.2 离线包 k8s-bundle.tar.gz

只能在**联网的 Linux 构建主机**上跑（需 `docker`/`nerdctl`、`curl`、`tar`；Cilium 还需 `helm`）。

```bash
cd scripts
./build-k8s-bundle.sh                          # 默认 v1.35.7 / amd64 / cilium
./build-k8s-bundle.sh --version v1.35.7 --arch amd64 --cni calico
./build-k8s-bundle.sh --arch amd64,arm64       # 一次出两个架构
./build-k8s-bundle.sh --split                  # 分卷（默认每卷 90m）+ 清单
./build-k8s-bundle.sh --help
```

常用参数：`--version`、`--arch`（逗号分隔可多架构）、`--cni`（cilium|calico）、`--addons`、
`--image-repo`、`--out`（默认 `./dist`）、`--split` / `--split-size`。
组件版本用环境变量覆盖：`CONTAINERD_VERSION` / `RUNC_VERSION` / `CNI_PLUGINS_VERSION` /
`INGRESS_NGINX_VERSION` / `METRICS_SERVER_VERSION` / `CERT_MANAGER_VERSION`。

产物：`dist/k8s-bundle-<版本>-<arch>-<cni>.tar.gz` 及 `.sha256`。

> 跨架构：在 amd64 主机上打 arm64 包时，用 host 架构的 kubeadm 列镜像清单（与架构无关），
> 再 `pull --platform linux/arm64` 拉目标架构镜像；无需 arm64 主机。

### 分卷（--split）

Gitee Release 单附件有大小上限。`--split` 把包切成 `<split-size>` 的分卷并生成 `.parts.json` 清单：

```
dist/
  k8s-bundle-v1.35.7-amd64-cilium.tar.gz              # 整包（本地留存）
  k8s-bundle-v1.35.7-amd64-cilium.tar.gz.sha256
  k8s-bundle-v1.35.7-amd64-cilium.tar.gz.part-000
  ...
  k8s-bundle-v1.35.7-amd64-cilium.tar.gz.parts.json   # 清单（archive/sha256/size/parts[]）
```

把**所有 `.part-*` 和 `.parts.json`** 传到同一路径前缀下，`bundleUrl` 填 **`.parts.json` 的直链**。
agent 识别到 `.parts.json` 后按 `parts[]` 顺序逐卷下载 → 拼接 → 校验 sha256 → 照常解包。

### bundleUrl 怎么填

任何 agent 能 `curl` 到的匿名 `http(s)://` 直链：对象存储（OSS/S3，内网更快）、Gitee/GitHub Release 附件、
内网 `python3 -m http.server`。arch 要对上目标机，CNI 要与 InstallSpec 一致。
只有首个 Master 建集群时要给；后续节点并入由面板复用集群里存的地址。

### add-on

`--addons` 决定打进离线包的 add-on（默认三个）：

```bash
./build-k8s-bundle.sh --addons ingress-nginx,metrics-server,cert-manager
./build-k8s-bundle.sh --addons ""
```

- `ingress-nginx`：南北向入口（baremetal 版，NodePort）。
- `metrics-server`：`kubectl top` / HPA；自动注入 `--kubelet-insecure-tls`。
- `cert-manager`：证书签发/续期。

装机时机：首 master 装完 CNI 后，按 InstallSpec.addons 逐个 `kubectl apply --server-side`。
InstallSpec 里的 add-on **必须**是离线包 `--addons` 已打进去的，否则首 master 装机失败。

---

## 4. 本地联调

```bash
go run ./cmd/starport-panel serve --bootstrap-token dev --data-dir /tmp/sp-panel
go run ./cmd/starport-agent --server http://127.0.0.1:8080 --token dev --data-dir /tmp/sp-agent
curl -s http://127.0.0.1:8080/api/v1/nodes
curl -s -X POST http://127.0.0.1:8080/api/v1/nodes/1/exec -d '{"script":"uname -a"}'   # → {"taskId":1}
curl -s http://127.0.0.1:8080/api/v1/tasks/1/logs
```

非 Linux 机器上 agent 能注册/连接，但 exec 会因无 `/bin/bash` 失败——链路验证足够，真实装机请用 Linux 节点。
