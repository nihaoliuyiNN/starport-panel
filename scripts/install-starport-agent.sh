#!/usr/bin/env bash
#
# install-starport-agent.sh — 在一台 Linux 机器上安装并常驻 starport-agent（节点侧代理）。
#
# 脚本与二进制都挂在 GitHub Release 上，节点上一条命令拉起：下载 starport-agent、装到 /usr/local/bin、
# 写 systemd 单元并 enable --now。
#
# 用法（需 root）：
#   curl -fsSL https://github.com/nihaoliuyiNN/starport-panel/releases/latest/download/install-starport-agent.sh | \
#     STARPORT_SERVER_URL=http://panel.example.com:8080 \
#     STARPORT_BOOTSTRAP_TOKEN=<引导令牌> \
#     bash
#
# 也可先下载脚本再带参数跑：
#   ./install-starport-agent.sh --server http://... --token <t> [--version v0.1.0] [--bin-url https://.../starport-agent]
#
# 环境变量（命令行同名参数优先）：
#   STARPORT_SERVER_URL       面板地址（如 http://panel.example.com:8080）        必填
#   STARPORT_BOOTSTRAP_TOKEN  引导令牌（面板 --bootstrap-token）                     必填
#   STARPORT_VERSION          要装的版本（Release tag），默认 latest
#   STARPORT_AGENT_BIN_URL    二进制下载地址；默认从 GitHub Release 取 starport-agent-linux-<arch>
#   STARPORT_DATA_DIR         身份/临时目录，默认 /var/lib/starport-agent
#   STARPORT_GH_PROXY         可选，GitHub 加速前缀（如 https://ghfast.top/），拼在 Release 下载地址前
#
set -euo pipefail

REPO="${STARPORT_REPO:-nihaoliuyiNN/starport-panel}"
GH_PROXY="${STARPORT_GH_PROXY:-}"
VERSION="${STARPORT_VERSION:-latest}"
SERVER="${STARPORT_SERVER_URL:-}"
TOKEN="${STARPORT_BOOTSTRAP_TOKEN:-}"
BIN_URL="${STARPORT_AGENT_BIN_URL:-}"
DATA_DIR="${STARPORT_DATA_DIR:-/var/lib/starport-agent}"
BIN_PATH="/usr/local/bin/starport-agent"
SVC="starport-agent"

# ── 解析命令行参数（覆盖环境变量）────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)   SERVER="$2"; shift 2 ;;
    --token)    TOKEN="$2"; shift 2 ;;
    --version)  VERSION="$2"; shift 2 ;;
    --bin-url)  BIN_URL="$2"; shift 2 ;;
    --gh-proxy) GH_PROXY="$2"; shift 2 ;;
    --data-dir) DATA_DIR="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

# ── 校验 ─────────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || { echo "需 root 运行（starport-agent 装机阶段要写系统盘）" >&2; exit 1; }
[[ -n "$SERVER"  ]] || { echo "缺少 --server / STARPORT_SERVER_URL" >&2; exit 1; }
[[ -n "$TOKEN"   ]] || { echo "缺少 --token / STARPORT_BOOTSTRAP_TOKEN" >&2; exit 1; }
if [[ -z "$BIN_URL" ]]; then
  case "$(uname -m)" in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "不支持的架构 $(uname -m)；请自行编译并用 --bin-url 指定" >&2; exit 1 ;;
  esac
  if [[ "$VERSION" == "latest" ]]; then
    BIN_URL="${GH_PROXY}https://github.com/$REPO/releases/latest/download/starport-agent-linux-$arch"
  else
    BIN_URL="${GH_PROXY}https://github.com/$REPO/releases/download/$VERSION/starport-agent-linux-$arch"
  fi
fi

dl() { # dl <url> <dest>：带进度条，连接超时 15s，失败重试 3 次
  if command -v curl >/dev/null 2>&1; then curl -fL# --connect-timeout 15 --retry 3 "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget --show-progress -qO "$2" "$1"
  else echo "缺少 curl/wget" >&2; exit 1; fi
}

echo "[install] 下载 starport-agent: $BIN_URL"
tmp="$(mktemp)"
dl "$BIN_URL" "$tmp"
install -m 0755 "$tmp" "$BIN_PATH"
rm -f "$tmp"
mkdir -p "$DATA_DIR"
echo "[install] 已安装 $BIN_PATH（$("$BIN_PATH" --version 2>/dev/null || echo 版本未知)）"

echo "[install] 写 systemd 单元 /etc/systemd/system/${SVC}.service"
cat > "/etc/systemd/system/${SVC}.service" <<EOF
[Unit]
Description=Starport Agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$BIN_PATH --server $SERVER --token $TOKEN --data-dir $DATA_DIR
Restart=always
RestartSec=3
# 装机会写系统盘/加载内核模块，必须 root
User=root

[Install]
WantedBy=multi-user.target
EOF

chmod 600 "/etc/systemd/system/${SVC}.service"   # 令牌在单元里，收紧读权限
systemctl daemon-reload
systemctl enable --now "$SVC"

echo "[install] 完成。查看日志： journalctl -u ${SVC} -f"
echo "[install] 正常应看到：注册成功 / 上线；面板节点列表将出现本机（online）"
