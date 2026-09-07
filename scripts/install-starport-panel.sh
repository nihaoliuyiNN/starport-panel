#!/usr/bin/env bash
#
# install-starport-panel.sh — 在一台 Linux 机器上安装并常驻 starport-panel（控制面）。
#
# 下载 starport-panel 二进制 → /usr/local/bin，写 /etc/starport-panel/env（令牌等配置）与 systemd 单元，
# enable --now，最后签发第一枚 API 令牌打印出来。重复执行会保留已有 env（含引导令牌），只更新二进制。
#
# 用法（需 root）：
#   curl -fsSL https://github.com/nihaoliuyiNN/starport-panel/releases/latest/download/install-starport-panel.sh | bash
#   ./install-starport-panel.sh [--version v0.1.0] [--bin-url https://.../starport-panel] [--http :8080] [--grpc :9192] \
#       [--bootstrap-token <t>] [--tls-cert /path/fullchain.pem --tls-key /path/privkey.pem] [--grpc-endpoints panel.example.com:9192]
#
# 环境变量（命令行同名参数优先）：
#   STARPORT_VERSION           要装的版本（Release tag），默认 latest
#   STARPORT_PANEL_BIN_URL     二进制下载地址；默认从 GitHub Release 取 starport-panel-linux-<arch>
#   STARPORT_HTTP_ADDR         HTTP 监听，默认 :8080
#   STARPORT_GRPC_ADDR         gRPC 监听，默认 :9192
#   STARPORT_BOOTSTRAP_TOKEN   agent 引导令牌；空则随机生成
#   STARPORT_DATA_DIR          状态目录，默认 /var/lib/starport-panel
#   STARPORT_TLS_CERT / STARPORT_TLS_KEY   可选，同时给出则 HTTP+gRPC 启用 TLS
#   STARPORT_GRPC_ENDPOINTS    可选，下发给 agent 的 gRPC 入口（面板在 LB/NAT 后时指定）
#   STARPORT_WITH_AGENT=1      可选，顺手把本机也装成节点（单机 / 面板机兼作 master 时用）
#   STARPORT_GH_PROXY          可选，优先使用的 GitHub 加速前缀（如 https://ghfast.top/）；不设也会在直连失败时
#                              自动尝试内置镜像列表（STARPORT_GH_MIRRORS 可覆盖）。会传给 agent 安装脚本
#
set -euo pipefail

REPO="${STARPORT_REPO:-nihaoliuyiNN/starport-panel}"
GH_PROXY="${STARPORT_GH_PROXY:-}"
VERSION="${STARPORT_VERSION:-latest}"
WITH_AGENT="${STARPORT_WITH_AGENT:-0}"
BIN_URL="${STARPORT_PANEL_BIN_URL:-}"
HTTP_ADDR="${STARPORT_HTTP_ADDR:-:8080}"
GRPC_ADDR="${STARPORT_GRPC_ADDR:-:9192}"
BOOT_TOKEN="${STARPORT_BOOTSTRAP_TOKEN:-}"
DATA_DIR="${STARPORT_DATA_DIR:-/var/lib/starport-panel}"
TLS_CERT="${STARPORT_TLS_CERT:-}"
TLS_KEY="${STARPORT_TLS_KEY:-}"
GRPC_EPS="${STARPORT_GRPC_ENDPOINTS:-}"
BIN_PATH="/usr/local/bin/starport-panel"
CONF_DIR="/etc/starport-panel"
ENV_FILE="$CONF_DIR/env"
SVC="starport-panel"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)         VERSION="$2"; shift 2 ;;
    --with-agent)      WITH_AGENT=1; shift ;;
    --gh-proxy)        GH_PROXY="$2"; shift 2 ;;
    --bin-url)         BIN_URL="$2"; shift 2 ;;
    --http)            HTTP_ADDR="$2"; shift 2 ;;
    --grpc)            GRPC_ADDR="$2"; shift 2 ;;
    --bootstrap-token) BOOT_TOKEN="$2"; shift 2 ;;
    --data-dir)        DATA_DIR="$2"; shift 2 ;;
    --tls-cert)        TLS_CERT="$2"; shift 2 ;;
    --tls-key)         TLS_KEY="$2"; shift 2 ;;
    --grpc-endpoints)  GRPC_EPS="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

[[ $EUID -eq 0 ]] || { echo "需 root 运行" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "需要 curl" >&2; exit 1; }
if [[ -n "$TLS_CERT" || -n "$TLS_KEY" ]]; then
  [[ -n "$TLS_CERT" && -n "$TLS_KEY" ]] || { echo "--tls-cert 与 --tls-key 须同时给出" >&2; exit 1; }
  [[ -r "$TLS_CERT" && -r "$TLS_KEY" ]] || { echo "证书/私钥文件不可读" >&2; exit 1; }
fi

# ── 下载：直连 GitHub 不通就自动换镜像 ─────────────────────────────
# 候选前缀顺序：用户指定的 STARPORT_GH_PROXY → 直连 → 内置镜像（可用 STARPORT_GH_MIRRORS 覆盖）。
# 探测阶段静默、限时；哪个通就用哪个下，带进度条。所有 curl 走 HTTP/1.1，规避劣质链路上的 HTTP/2 帧错误。
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

# 下到目标目录旁边再校验：/tmp 可能 noexec，且 mktemp 出来的文件没有执行位
mkdir -p "$(dirname "$BIN_PATH")"
tmp="$BIN_PATH.download"
if [[ -n "$BIN_URL" ]]; then
  echo "[install] 下载 $BIN_URL"
  $CURL -# --connect-timeout 10 -o "$tmp" "$BIN_URL"
else
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    *) echo "面板目前只发 linux/amd64 二进制（本机 $(uname -m)）；请自行编译并用 --bin-url 指定" >&2; exit 1 ;;
  esac
  if [[ "$VERSION" == "latest" ]]; then REL="https://github.com/$REPO/releases/latest/download"
  else REL="https://github.com/$REPO/releases/download/$VERSION"; fi
  asset="starport-panel-linux-$arch"
  gh_dl "$REL/$asset" "$tmp"
  # 校验 sha256：走镜像时尤其要做；SHA256SUMS 拉不到只告警
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
if ! "$tmp" version >/dev/null 2>&1; then
  echo "下载的文件不是可执行的 starport-panel（$(stat -c %s "$tmp") 字节，开头：$(head -c 64 "$tmp" | tr -cd '[:print:]' | head -c 64)）" >&2
  rm -f "$tmp"; exit 1
fi
mv -f "$tmp" "$BIN_PATH"
mkdir -p "$DATA_DIR" "$CONF_DIR"
echo "[install] 已安装 $BIN_PATH（$("$BIN_PATH" version)）"

# ── 配置：已有 env 则保留（重复执行只升级二进制） ─────────────────
if [[ -f "$ENV_FILE" ]]; then
  echo "[install] 保留已有配置 $ENV_FILE"
else
  if [[ -z "$BOOT_TOKEN" ]]; then
    BOOT_TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    echo "[install] 已生成随机引导令牌"
  fi
  {
    echo "STARPORT_HTTP_ADDR=$HTTP_ADDR"
    echo "STARPORT_GRPC_ADDR=$GRPC_ADDR"
    echo "STARPORT_DATA_DIR=$DATA_DIR"
    echo "STARPORT_BOOTSTRAP_TOKEN=$BOOT_TOKEN"
    [[ -n "$TLS_CERT" ]] && echo "STARPORT_TLS_CERT=$TLS_CERT"
    [[ -n "$TLS_KEY"  ]] && echo "STARPORT_TLS_KEY=$TLS_KEY"
    [[ -n "$GRPC_EPS" ]] && echo "STARPORT_GRPC_ENDPOINTS=$GRPC_EPS"
  } > "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  echo "[install] 已写配置 $ENV_FILE"
fi

echo "[install] 写 systemd 单元 /etc/systemd/system/${SVC}.service"
cat > "/etc/systemd/system/${SVC}.service" <<EOF
[Unit]
Description=Starport Panel
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_PATH serve
Restart=always
RestartSec=3
User=root
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now "$SVC"
systemctl restart "$SVC"   # 升级二进制时也生效

# ── 首枚 API 令牌 ───────────────────────────────────────────────
# shellcheck disable=SC1090
source "$ENV_FILE"
if ! "$BIN_PATH" token list --data-dir "$DATA_DIR" 2>/dev/null | awk 'NR>1 && $NF=="active"{found=1} END{exit !found}'; then
  echo
  echo "[install] 签发首枚 API 令牌（用于登录 Web UI / 调 API）："
  "$BIN_PATH" token create --name admin --data-dir "$DATA_DIR"
fi

scheme=http; [[ -n "${STARPORT_TLS_CERT:-}" ]] && scheme=https

# ── 可选：本机也装成节点 ───────────────────────────────────────────
if [[ "$WITH_AGENT" == "1" ]]; then
  port="${STARPORT_HTTP_ADDR##*:}"
  if [[ "$VERSION" == "latest" ]]; then
    agent_sh="${GH_PROXY}https://github.com/$REPO/releases/latest/download/install-starport-agent.sh"
  else
    agent_sh="${GH_PROXY}https://github.com/$REPO/releases/download/$VERSION/install-starport-agent.sh"
  fi
  echo
  echo "[install] 本机同时装为节点（agent 连 ${scheme}://127.0.0.1:${port}）"
  tmp="$(mktemp)"
  dl "$agent_sh" "$tmp"
  STARPORT_SERVER_URL="${scheme}://127.0.0.1:${port}" STARPORT_BOOTSTRAP_TOKEN="$STARPORT_BOOTSTRAP_TOKEN" \
    STARPORT_VERSION="$VERSION" STARPORT_REPO="$REPO" STARPORT_GH_PROXY="$GH_PROXY" bash "$tmp"
  rm -f "$tmp"
fi

echo
echo "[install] 完成。"
echo "  Web UI / API : ${scheme}://<本机地址>${STARPORT_HTTP_ADDR}"
echo "  agent 引导令牌: ${STARPORT_BOOTSTRAP_TOKEN}   （纳管节点时用，见 install-starport-agent.sh）"
echo "  日志          : journalctl -u ${SVC} -f"
echo "  备份          : $BIN_PATH backup --data-dir $DATA_DIR"
