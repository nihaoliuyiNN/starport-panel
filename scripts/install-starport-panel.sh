#!/usr/bin/env bash
#
# install-starport-panel.sh — 在一台 Linux 机器上安装并常驻 starport-panel（控制面）。
#
# 下载 starport-panel 二进制 → /usr/local/bin，写 /etc/starport-panel/env（令牌等配置）与 systemd 单元，
# enable --now，最后签发第一枚 API 令牌打印出来。重复执行会保留已有 env（含引导令牌），只更新二进制。
#
# 用法（需 root）：
#   STARPORT_PANEL_BIN_URL=https://.../starport-panel bash install-starport-panel.sh
#   ./install-starport-panel.sh --bin-url https://.../starport-panel [--http :8080] [--grpc :9192] \
#       [--bootstrap-token <t>] [--tls-cert /path/fullchain.pem --tls-key /path/privkey.pem] [--grpc-endpoints panel.example.com:9192]
#
# 环境变量（命令行同名参数优先）：
#   STARPORT_PANEL_BIN_URL     starport-panel 二进制下载地址        必填
#   STARPORT_HTTP_ADDR         HTTP 监听，默认 :8080
#   STARPORT_GRPC_ADDR         gRPC 监听，默认 :9192
#   STARPORT_BOOTSTRAP_TOKEN   agent 引导令牌；空则随机生成
#   STARPORT_DATA_DIR          状态目录，默认 /var/lib/starport-panel
#   STARPORT_TLS_CERT / STARPORT_TLS_KEY   可选，同时给出则 HTTP+gRPC 启用 TLS
#   STARPORT_GRPC_ENDPOINTS    可选，下发给 agent 的 gRPC 入口（面板在 LB/NAT 后时指定）
#
set -euo pipefail

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
[[ -n "$BIN_URL" ]] || { echo "缺少 --bin-url / STARPORT_PANEL_BIN_URL" >&2; exit 1; }
if [[ -n "$TLS_CERT" || -n "$TLS_KEY" ]]; then
  [[ -n "$TLS_CERT" && -n "$TLS_KEY" ]] || { echo "--tls-cert 与 --tls-key 须同时给出" >&2; exit 1; }
  [[ -r "$TLS_CERT" && -r "$TLS_KEY" ]] || { echo "证书/私钥文件不可读" >&2; exit 1; }
fi

dl() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
  else echo "缺少 curl/wget" >&2; exit 1; fi
}

echo "[install] 下载 starport-panel: $BIN_URL"
tmp="$(mktemp)"
dl "$BIN_URL" "$tmp"
"$tmp" version >/dev/null 2>&1 || { echo "下载的文件不是可执行的 starport-panel" >&2; rm -f "$tmp"; exit 1; }
install -m 0755 "$tmp" "$BIN_PATH"
rm -f "$tmp"
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
echo
echo "[install] 完成。"
echo "  Web UI / API : ${scheme}://<本机地址>${STARPORT_HTTP_ADDR}"
echo "  agent 引导令牌: ${STARPORT_BOOTSTRAP_TOKEN}   （纳管节点时用，见 install-starport-agent.sh）"
echo "  日志          : journalctl -u ${SVC} -f"
echo "  备份          : $BIN_PATH backup --data-dir $DATA_DIR"
