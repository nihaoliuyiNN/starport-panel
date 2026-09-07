# starport-panel

自托管的 Kubernetes 面板。一个二进制起控制面，节点上装一个 agent，面板经 agent 用 kubeadm 把集群装起来，然后管集群、装应用。

- 节点只出站连面板（gRPC 长连），不开端口、不用 SSH。
- 装机制品可以打成离线包，节点不需要外网。
- 面板状态就一个 SQLite 文件。
- 已有集群给个 kubeconfig 也能接管，只是不能经面板加减节点。

Web UI 内嵌在面板二进制里，只调公开 API；API 有 OpenAPI 描述（`GET /api/v1/openapi.yaml`），测试会校验它和路由表一致。

目前所有功能都写完了，还没在真实多节点环境跑过完整装机。

## 部署

一台 Linux（amd64）机器，root 执行：

```bash
curl -fsSL https://github.com/nihaoliuyiNN/starport-panel/releases/latest/download/install-starport-panel.sh | bash
```

结束时会打印一枚 API 令牌，用它登录 `http://<这台机器>:8080`。剩下的都在页面里做：

- **节点** 页点「纳管节点」，把弹出的命令拷到每台要管的机器上跑一遍，几秒后上线。
- **集群** 页新建集群，选一台在线节点做 `first-master`，看任务日志装完，再加 `join-master` / `worker`。
- 集群详情页里是工作负载、YAML、Helm 应用、终端、用量、审计。

只有一台机器？加 `STARPORT_WITH_AGENT=1`，面板机自己也装成节点，直接拿它建单机集群：

```bash
curl -fsSL https://github.com/nihaoliuyiNN/starport-panel/releases/latest/download/install-starport-panel.sh | STARPORT_WITH_AGENT=1 bash
```

节点要能访问面板的 8080 和 9192；节点没外网就得先用 `scripts/build-k8s-bundle.sh` 打离线包。TLS、备份、离线包、钉版本、自己编二进制、发版（推 tag 即可）见 [docs/build.md](docs/build.md)；接口见 [docs/api.md](docs/api.md)。

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
