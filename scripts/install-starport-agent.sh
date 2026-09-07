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
#   STARPORT_GH_PROXY         可选，优先使用的 GitHub 加速前缀；不设也会在直连失败时自动尝试内置镜像（STARPORT_GH_MIRRORS 可覆盖）
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
command -v curl >/dev/null 2>&1 || { echo "需要 curl" >&2; exit 1; }

# ── 下载：直连 GitHub 不通就自动换镜像（与 install-starport-panel.sh 同一套逻辑） ──
MIRRORS="${STARPORT_GH_MIRRORS:-https://ghfast.top/ https://gh-proxy.com/ https://mirror.ghproxy.com/}"
CURL="curl -fL --http1.1"
gh_dl() { # gh_dl <github 直链> <dest>
  local url="$1" dest="$2" p full
  for p in ${GH_PROXY:+"$GH_PROXY"} "" $MIRRORS; do
    full="${p}${url}"
    if $CURL -s --connect-timeout 6 --max-time 10 -r 0-0 -o /dev/null "$full" 2>/dev/null; then
      echo "[install] 下载 $full"
      if $CURL -# --connect-timeout 10 -o "$dest" "$full"; then return 0; fi
      echo "[install] 下载中断，换下一个源"
    else
      echo "[install] ${p:-直连 github.com} 连不上，换下一个源"
    fi
  done
  echo "所有下载源都不可用；可自行下载二进制后用 --bin-url 指定，或设 STARPORT_GH_PROXY" >&2
  return 1
}

mkdir -p "$(dirname "$BIN_PATH")"
tmp="$BIN_PATH.download"
if [[ -n "$BIN_URL" ]]; then
  echo "[install] 下载 $BIN_URL"
  $CURL -# --connect-timeout 10 -o "$tmp" "$BIN_URL"
else
  case "$(uname -m)" in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "不支持的架构 $(uname -m)；请自行编译并用 --bin-url 指定" >&2; exit 1 ;;
  esac
  if [[ "$VERSION" == "latest" ]]; then REL="https://github.com/$REPO/releases/latest/download"
  else REL="https://github.com/$REPO/releases/download/$VERSION"; fi
  asset="starport-agent-linux-$arch"
  gh_dl "$REL/$asset" "$tmp"
  sums="$tmp.sums"
  if gh_dl "$REL/SHA256SUMS" "$sums" >/dev/null 2>&1; then
    want="$(awk -v f="$asset" '$2==f{print $1}' "$sums")"; rm -f "$sums"
    got="$(sha256sum "$tmp" | awk '{print $1}')"
    if [[ -n "$want" && "$want" != "$got" ]]; then
      echo "sha256 不匹配（期望 $want，实际 $got），文件可能损坏或被篡改" >&2; rm -f "$tmp"; exit 1
    fi
    echo "[install] sha256 校验通过"
  else
    echo "[install] 未能获取 SHA256SUMS，跳过校验"
  fi
fi
chmod 0755 "$tmp"
if ! "$tmp" --version >/dev/null 2>&1; then
  echo "下载的文件不是可执行的 starport-agent（$(stat -c %s "$tmp") 字节）" >&2
  rm -f "$tmp"; exit 1
fi
# 已在运行的旧 agent 会被 systemctl restart 接管新二进制
mv -f "$tmp" "$BIN_PATH"
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
