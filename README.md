# starport-panel

自托管的 Kubernetes 面板。一个二进制起控制面，节点上装一个 agent，面板经 agent 用 kubeadm 把集群装起来，然后管集群、装应用。

- 节点只出站连面板（gRPC 长连），不开端口、不用 SSH。
- 装机制品可以打成离线包，节点不需要外网。
- 面板状态就一个 SQLite 文件。
- 已有集群给个 kubeconfig 也能接管，只是不能经面板加减节点。

Web UI 内嵌在面板二进制里，只调公开 API；API 有 OpenAPI 描述（`GET /api/v1/openapi.yaml`），测试会校验它和路由表一致。

目前所有功能都写完了，还没在真实多节点环境跑过完整装机。

## 部署

需要一台跑面板的 Linux 机器（amd64），和若干台要纳管的 Linux 节点（amd64 / arm64）。节点能访问面板的 8080（注册）和 9192（gRPC）即可。

**1. 装面板**

```bash
curl -fsSL https://github.com/nihaoliuyiNN/starport-panel/releases/latest/download/install-starport-panel.sh | bash
```

装到 `/usr/local/bin`，配置写 `/etc/starport-panel/env`，注册 systemd 服务，最后打印两样东西：agent 引导令牌和第一枚 API 令牌。浏览器打开 `http://<面板>:8080`，用 API 令牌登录。

要 TLS 的话在 env 里加 `STARPORT_TLS_CERT` / `STARPORT_TLS_KEY`（得是节点信得过的证书），或者前面放反向代理，但 9192 是 agent 直连的，代理得能透传 gRPC。

**2. 装节点**

每台节点上：

```bash
curl -fsSL https://github.com/nihaoliuyiNN/starport-panel/releases/latest/download/install-starport-agent.sh | \
  STARPORT_SERVER_URL=http://<面板>:8080 \
  STARPORT_BOOTSTRAP_TOKEN=<上一步打印的引导令牌> bash
```

几秒后面板「节点」页应该看到它在线。

想钉版本加 `STARPORT_VERSION=v0.1.0`；想用自己编的二进制加 `STARPORT_PANEL_BIN_URL` / `STARPORT_AGENT_BIN_URL`。自己编：`make ui && make agent-linux`，面板用 `GOOS=linux go build ./cmd/starport-panel`。发版走 `scripts/publish-release.ps1`，它把编译、建 GitHub Release、传附件、校验直链一起做完。

**3. 建集群**

在 UI「集群」页新建：选在线模式（节点有外网）或离线包模式（先用 `scripts/build-k8s-bundle.sh` 打包传到 http 位置，填 `bundleUrl`），然后给集群加第一台 `first-master`，装机日志在任务抽屉里滚。控制面就绪后再加 `join-master` / `worker`。

之后的事——工作负载、YAML、Helm 应用、终端、用量、审计——都在集群详情页。

参数、备份恢复、离线包、发版细节见 [docs/build.md](docs/build.md)；接口见 [docs/api.md](docs/api.md)。

## 本地开发

```bash
go run ./cmd/starport-panel serve --bootstrap-token dev --data-dir /tmp/sp --insecure-no-auth
go run ./cmd/starport-agent --server http://127.0.0.1:8080 --token dev --data-dir /tmp/sp-agent
make ui-dev                  # :5173，/api 代理到 :8080
make test
```

非 Linux 上 agent 能注册、连上，但 exec / 装机跑不了。

## 代码布局

```
cmd/starport-panel        控制面：serve / token / backup
cmd/starport-agent        节点代理
internal/panel            HTTP 路由、鉴权、审计、gRPC 服务端、openapi.yaml
internal/panel/agenthub   agent 连接表与请求关联
internal/panel/store      SQLite，所有 SQL 都在这
internal/panel/cluster    建集群 / 加减节点 / 接管
internal/panel/kube       client-go 直连 apiserver
internal/panel/helm       Helm SDK 封装
internal/panel/task       异步任务与日志
internal/agent            agent 运行时
internal/installer        装机引擎，只依赖标准库
proto/agent/v1            面板 ↔ agent 契约
web/                      React + antd UI，make ui 后内嵌进面板
```

约定见 [AGENTS.md](AGENTS.md)。

## 许可证

[PolyForm Noncommercial 1.0.0](LICENSE)：非商业用途随便用改发，商业使用需另行授权。
