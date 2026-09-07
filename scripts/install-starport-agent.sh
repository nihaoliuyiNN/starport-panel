#!/usr/bin/env bash
#
# install-starport-agent.sh — 在一台 Linux 机器上安装并常驻 starport-agent（节点侧代理）。
#
# 设计：脚本本身托管在 Gitee（raw 或 releases 附件），ECS 上一条命令即可拉起——脚本再从
# Gitee 下载 starport-agent 二进制、装到 /usr/local/bin、写 systemd 单元并 enable --now。
#
# 用法（在 ECS 上，需 root）：
#   curl -fsSL https://gitee.com/<用户>/<仓库>/raw/master/install-starport-agent.sh | \
#     STARPORT_SERVER_URL=http://panel.example.com:8080 \
#     STARPORT_BOOTSTRAP_TOKEN=<引导令牌> \
#     STARPORT_AGENT_BIN_URL=https://gitee.com/<用户>/<仓库>/raw/master/starport-agent \
#     bash
#
# 也可先下载脚本再带参数跑：
#   ./install-starport-agent.sh --server http://... --token <t> --bin-url https://.../starport-agent
#
# 环境变量（命令行同名参数优先）：
#   STARPORT_SERVER_URL       面板地址（如 http://panel.example.com:8080）        必填
#   STARPORT_BOOTSTRAP_TOKEN  引导令牌（面板 --bootstrap-token）                     必填
#   STARPORT_AGENT_BIN_URL    starport-agent 二进制的 Gitee 直链                      必填
#   STARPORT_DATA_DIR         身份/临时目录，默认 /var/lib/starport-agent
#
set -euo pipefail

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
    --bin-url)  BIN_URL="$2"; shift 2 ;;
    --data-dir) DATA_DIR="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

# ── 校验 ─────────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || { echo "需 root 运行（starport-agent 装机阶段要写系统盘）" >&2; exit 1; }
[[ -n "$SERVER"  ]] || { echo "缺少 --server / STARPORT_SERVER_URL" >&2; exit 1; }
[[ -n "$TOKEN"   ]] || { echo "缺少 --token / STARPORT_BOOTSTRAP_TOKEN" >&2; exit 1; }
[[ -n "$BIN_URL" ]] || { echo "缺少 --bin-url / STARPORT_AGENT_BIN_URL（starport-agent Gitee 直链）" >&2; exit 1; }

dl() { # dl <url> <dest>：优先 curl，回退 wget
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
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
