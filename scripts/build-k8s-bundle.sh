#!/usr/bin/env bash
#
# build-k8s-bundle.sh — 生成 starport-agent 内置装机用的离线包 k8s-bundle.tar.gz
#
# 产出布局（与 internal/installer 的 bundle 消费严格对齐）：
#   bin/                kubeadm kubelet kubectl containerd containerd-shim-runc-v2 ctr runc
#   bin/cni/            loopback bridge portmap host-local ...（cni-plugins，落 /opt/cni/bin）
#   images/*.tar        kubeadm 控制面镜像 + pause + CNI 镜像（ctr -n k8s.io images import）
#   cni/*.yaml          CNI 清单（kubectl apply）
#   addons/*.yaml       add-on 清单（首 master 装完 CNI 后按需 kubectl apply）
#   systemd/            containerd.service kubelet.service 10-kubeadm.conf
#
# 运行环境：Linux 构建主机（Win 用 WSL2），需联网 + 具备 docker（或 nerdctl）用于拉取并导出镜像。
# 用法：
#   ./build-k8s-bundle.sh [--version v1.35.7] [--arch amd64|arm64|amd64,arm64] [--cni cilium|calico]
#                         [--cni-version X] [--addons ingress-nginx,metrics-server,cert-manager]
#                         [--image-repo registry.k8s.io] [--out DIR]
#                         [--split] [--split-size 90m]
# 多架构：--arch 传逗号分隔（如 amd64,arm64），逐个架构各出一个离线包。
# 分卷：  --split 把每个离线包切成 <split-size 的分卷（默认 90m，绕开 Gitee 单附件上限），
#         同时生成 <archive>.parts.json 清单；装机时把该 .parts.json 的 URL 作为 bundleUrl，
#         agent 会按清单下载各分卷、顺序拼接并做 sha256 校验。
# 环境变量可覆盖组件版本：CONTAINERD_VERSION / RUNC_VERSION / CNI_PLUGINS_VERSION
#                        INGRESS_NGINX_VERSION / METRICS_SERVER_VERSION / CERT_MANAGER_VERSION
#
set -euo pipefail

# ── 默认参数 ────────────────────────────────────────────────
K8S_VERSION="v1.35.7"
ARCH="amd64"             # 可逗号分隔多架构：amd64,arm64
CNI="cilium"
CNI_VERSION=""            # 空则按 CNI 取内置默认
IMAGE_REPO=""             # 空则用官方 registry.k8s.io
OUT_DIR="$(pwd)/dist"
SPLIT=false
SPLIT_SIZE="90m"          # 单卷上限，默认 90m（安全低于 Gitee ~100MB 单附件上限）

CONTAINERD_VERSION="${CONTAINERD_VERSION:-1.7.22}"
RUNC_VERSION="${RUNC_VERSION:-1.1.14}"
CNI_PLUGINS_VERSION="${CNI_PLUGINS_VERSION:-1.5.1}"
DEFAULT_CILIUM_VERSION="1.16.5"
DEFAULT_CALICO_VERSION="v3.29.1"

# add-on：逗号分隔，空则不打包 add-on。支持 ingress-nginx / metrics-server / cert-manager。
# 与 starport-agent installer 的 addons/<name>.yaml 布局对齐；镜像随 images/ 一并离线导入。
ADDONS="${ADDONS:-ingress-nginx,metrics-server,cert-manager}"
INGRESS_NGINX_VERSION="${INGRESS_NGINX_VERSION:-controller-v1.11.3}"
METRICS_SERVER_VERSION="${METRICS_SERVER_VERSION:-v0.7.2}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.2}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)      K8S_VERSION="$2"; shift 2;;
    --arch)         ARCH="$2"; shift 2;;
    --cni)          CNI="$2"; shift 2;;
    --cni-version)  CNI_VERSION="$2"; shift 2;;
    --addons)       ADDONS="$2"; shift 2;;
    --image-repo)   IMAGE_REPO="$2"; shift 2;;
    --out)          OUT_DIR="$2"; shift 2;;
    --split)        SPLIT=true; shift;;
    --split-size)   SPLIT_SIZE="$2"; shift 2;;
    -h|--help)      grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//'; exit 0;;
    *) echo "未知参数: $1" >&2; exit 1;;
  esac
done

[[ "$K8S_VERSION" == v* ]] || K8S_VERSION="v${K8S_VERSION}"
if [[ -z "$CNI_VERSION" ]]; then
  [[ "$CNI" == "calico" ]] && CNI_VERSION="$DEFAULT_CALICO_VERSION" || CNI_VERSION="$DEFAULT_CILIUM_VERSION"
fi

# ── 依赖探测 ────────────────────────────────────────────────
need() { command -v "$1" >/dev/null 2>&1 || { echo "缺少依赖: $1" >&2; exit 1; }; }
need curl
need tar
$SPLIT && need split

PULLER=""
if command -v docker >/dev/null 2>&1; then PULLER="docker";
elif command -v nerdctl >/dev/null 2>&1; then PULLER="nerdctl";
else echo "需要 docker 或 nerdctl 用于拉取/导出镜像" >&2; exit 1; fi

# 构建主机架构：跨架构打包时用它跑 kubeadm 列镜像（arm64 kubeadm 在 amd64 host 上跑不了，
# 但镜像清单本身与架构无关，用 host 架构的 kubeadm 列出、再按目标架构 pull 即可）。
case "$(uname -m)" in
  x86_64|amd64)  HOST_ARCH="amd64";;
  aarch64|arm64) HOST_ARCH="arm64";;
  *)             HOST_ARCH="amd64";;
esac

log() { echo -e "\033[36m[bundle]\033[0m $*"; }

# 拉取并导出镜像为 tar（docker/nerdctl save，ctr import 兼容 docker-archive）
# 依赖调用方（build_one）设置的 local ARCH / STAGE（bash 动态作用域可见）。
save_image() {
  local img="$1"
  local safe; safe="$(echo "$img" | tr '/:@' '___')"
  log "拉取镜像 $img ($ARCH)"
  "$PULLER" pull --platform "linux/$ARCH" "$img" >/dev/null
  "$PULLER" save "$img" -o "$STAGE/images/${safe}.tar"
}

dl() { # dl URL DEST
  log "下载 $(basename "$2") ← $1"
  curl -fsSL "$1" -o "$2"
}

collect_images_from_yaml() { grep -oE 'image: *"?[^"[:space:]]+' "$1" | sed -E 's/image: *"?//' | sort -u; }

# ── 分卷 + 清单 ─────────────────────────────────────────────
split_bundle() { # split_bundle OUT_FILE SHA
  local out_file="$1" sha="$2"
  log "分卷 $(basename "$out_file")（每卷 $SPLIT_SIZE）"
  rm -f "${out_file}".part-* "${out_file}.parts.json"
  # -d 数字后缀、-a 3 → part-000 part-001 ...（最多 1000 卷）
  split -b "$SPLIT_SIZE" -d -a 3 "$out_file" "${out_file}.part-"
  local size; size="$(stat -c%s "$out_file")"
  local parts_json="" p bn
  for p in "${out_file}".part-*; do
    bn="$(basename "$p")"
    parts_json="${parts_json:+$parts_json,}\"${bn}\""
  done
  cat > "${out_file}.parts.json" <<EOF
{"archive":"$(basename "$out_file")","sha256":"${sha}","size":${size},"parts":[${parts_json}]}
EOF
  local n; n="$(ls -1 "${out_file}".part-* | wc -l | tr -d ' ')"
  log "分卷完成：${n} 卷 + $(basename "$out_file").parts.json（bundleUrl 填这个 .parts.json 的地址）"
}

# ── 单架构构建 ──────────────────────────────────────────────
build_one() {
  local ARCH="$1"                    # 覆盖全局，供 save_image/dl 动态引用
  local STAGE; STAGE="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$STAGE'" RETURN
  mkdir -p "$STAGE/bin/cni" "$STAGE/images" "$STAGE/cni" "$STAGE/systemd" "$STAGE/addons"

  log "===== 构建 $K8S_VERSION / $ARCH / $CNI@$CNI_VERSION ====="

  # 1) kube 二进制（目标架构，进包）
  local KBASE="https://dl.k8s.io/release/${K8S_VERSION}/bin/linux/${ARCH}"
  local b
  for b in kubeadm kubelet kubectl; do
    dl "${KBASE}/${b}" "$STAGE/bin/${b}"
    chmod +x "$STAGE/bin/${b}"
  done

  # 2) containerd / runc / ctr
  local CTD_TGZ="$STAGE/containerd.tgz"
  dl "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz" "$CTD_TGZ"
  tar -xzf "$CTD_TGZ" -C "$STAGE/bin" --strip-components=1 \
    bin/containerd bin/ctr bin/containerd-shim-runc-v2
  chmod +x "$STAGE/bin/containerd" "$STAGE/bin/ctr" "$STAGE/bin/containerd-shim-runc-v2"
  rm -f "$CTD_TGZ"

  dl "https://github.com/opencontainers/runc/releases/download/v${RUNC_VERSION}/runc.${ARCH}" "$STAGE/bin/runc"
  chmod +x "$STAGE/bin/runc"

  # 3) CNI 插件（/opt/cni/bin）
  local CNI_TGZ="$STAGE/cni-plugins.tgz"
  dl "https://github.com/containernetworking/plugins/releases/download/v${CNI_PLUGINS_VERSION}/cni-plugins-linux-${ARCH}-v${CNI_PLUGINS_VERSION}.tgz" "$CNI_TGZ"
  tar -xzf "$CNI_TGZ" -C "$STAGE/bin/cni"
  chmod +x "$STAGE/bin/cni/"* || true
  rm -f "$CNI_TGZ"

  # 4) kubeadm 控制面镜像清单（跨架构用 host 架构 kubeadm 来列）
  local LIST_KUBEADM="$STAGE/bin/kubeadm"
  if [[ "$ARCH" != "$HOST_ARCH" ]]; then
    dl "https://dl.k8s.io/release/${K8S_VERSION}/bin/linux/${HOST_ARCH}/kubeadm" "$STAGE/kubeadm-host"
    chmod +x "$STAGE/kubeadm-host"
    LIST_KUBEADM="$STAGE/kubeadm-host"
  fi
  log "解析 kubeadm 镜像清单（$K8S_VERSION）"
  local IMG_ARGS=(--kubernetes-version "$K8S_VERSION")
  [[ -n "$IMAGE_REPO" ]] && IMG_ARGS+=(--image-repository "$IMAGE_REPO")
  local CORE_IMAGES img
  mapfile -t CORE_IMAGES < <("$LIST_KUBEADM" config images list "${IMG_ARGS[@]}")
  for img in "${CORE_IMAGES[@]}"; do save_image "$img"; done
  rm -f "$STAGE/kubeadm-host"

  # 5) CNI 清单 + 镜像
  if [[ "$CNI" == "calico" ]]; then
    dl "https://raw.githubusercontent.com/projectcalico/calico/${CNI_VERSION}/manifests/calico.yaml" "$STAGE/cni/calico.yaml"
    while read -r img; do [[ -n "$img" ]] && save_image "$img"; done < <(collect_images_from_yaml "$STAGE/cni/calico.yaml")
  else
    need helm
    log "helm template 渲染 Cilium $CNI_VERSION"
    helm repo add cilium https://helm.cilium.io >/dev/null 2>&1 || true
    helm repo update >/dev/null
    helm template cilium cilium/cilium --version "$CNI_VERSION" \
      --namespace kube-system > "$STAGE/cni/cilium.yaml"
    while read -r img; do [[ -n "$img" ]] && save_image "$img"; done < <(collect_images_from_yaml "$STAGE/cni/cilium.yaml")
  fi

  # 5.5) add-on 清单 + 镜像
  local ADDON_LIST addon
  IFS=',' read -ra ADDON_LIST <<< "$ADDONS"
  for addon in "${ADDON_LIST[@]}"; do
    addon="$(echo "$addon" | xargs)"
    [[ -z "$addon" ]] && continue
    case "$addon" in
      ingress-nginx)
        dl "https://raw.githubusercontent.com/kubernetes/ingress-nginx/${INGRESS_NGINX_VERSION}/deploy/static/provider/baremetal/deploy.yaml" \
           "$STAGE/addons/ingress-nginx.yaml"
        ;;
      metrics-server)
        dl "https://github.com/kubernetes-sigs/metrics-server/releases/download/${METRICS_SERVER_VERSION}/components.yaml" \
           "$STAGE/addons/metrics-server.yaml"
        sed -i '/- --secure-port=10250/a\        - --kubelet-insecure-tls' "$STAGE/addons/metrics-server.yaml"
        ;;
      cert-manager)
        dl "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" \
           "$STAGE/addons/cert-manager.yaml"
        ;;
      *)
        echo "未知 add-on: $addon（支持 ingress-nginx/metrics-server/cert-manager）" >&2; exit 1 ;;
    esac
    while read -r img; do [[ -n "$img" ]] && save_image "$img"; done \
      < <(collect_images_from_yaml "$STAGE/addons/${addon}.yaml")
  done

  # 6) systemd 单元（与 installer 内置模板一致，保证离线自洽）
  cat > "$STAGE/systemd/containerd.service" <<'EOF'
[Unit]
Description=containerd container runtime
Documentation=https://containerd.io
After=network.target local-fs.target

[Service]
ExecStartPre=-/sbin/modprobe overlay
ExecStart=/usr/local/bin/containerd
Type=notify
Delegate=yes
KillMode=process
Restart=always
RestartSec=5
LimitNPROC=infinity
LimitCORE=infinity
LimitNOFILE=infinity
TasksMax=infinity
OOMScoreAdjust=-999

[Install]
WantedBy=multi-user.target
EOF

  cat > "$STAGE/systemd/kubelet.service" <<'EOF'
[Unit]
Description=kubelet: The Kubernetes Node Agent
Documentation=https://kubernetes.io/docs/
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/kubelet
Restart=always
StartLimitInterval=0
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

  cat > "$STAGE/systemd/10-kubeadm.conf" <<'EOF'
[Service]
Environment="KUBELET_KUBECONFIG_ARGS=--bootstrap-kubeconfig=/etc/kubernetes/bootstrap-kubelet.conf --kubeconfig=/etc/kubernetes/kubelet.conf"
Environment="KUBELET_CONFIG_ARGS=--config=/var/lib/kubelet/config.yaml"
EnvironmentFile=-/var/lib/kubelet/kubeadm-flags.env
EnvironmentFile=-/etc/default/kubelet
ExecStart=
ExecStart=/usr/local/bin/kubelet $KUBELET_KUBECONFIG_ARGS $KUBELET_CONFIG_ARGS $KUBELET_KUBEADM_ARGS $KUBELET_EXTRA_ARGS
EOF

  # 7) 打包
  local OUT_FILE="$OUT_DIR/k8s-bundle-${K8S_VERSION}-${ARCH}-${CNI}.tar.gz"
  log "打包 → $OUT_FILE"
  tar -czf "$OUT_FILE" -C "$STAGE" bin images cni systemd addons

  local SHA; SHA="$(sha256sum "$OUT_FILE" | awk '{print $1}')"
  echo "$SHA  $(basename "$OUT_FILE")" > "${OUT_FILE}.sha256"

  log "完成($ARCH)：$OUT_FILE"
  log "sha256: $SHA"
  log "镜像数：$(ls -1 "$STAGE/images" | wc -l)  | CNI：$CNI@$CNI_VERSION  | add-on：${ADDONS:-无}"

  if $SPLIT; then
    split_bundle "$OUT_FILE" "$SHA"
  fi
}

mkdir -p "$OUT_DIR"

# ── 逐架构构建 ──────────────────────────────────────────────
IFS=',' read -ra ARCH_LIST <<< "$ARCH"
for a in "${ARCH_LIST[@]}"; do
  a="$(echo "$a" | xargs)"
  [[ -z "$a" ]] && continue
  case "$a" in amd64|arm64) ;; *) echo "不支持的架构: $a（仅 amd64/arm64）" >&2; exit 1;; esac
  build_one "$a"
done

echo
if $SPLIT; then
  echo "分卷已生成。上传每个 .part-* 与对应的 .parts.json 到 Gitee/OSS，"
  echo "创建集群时把 .parts.json 的 URL 作为『离线包地址(bundleUrl)』填入即可。"
else
  echo "把 .tar.gz 放到 agent 能访问的静态 HTTP 地址，创建集群时填其 URL 作为 bundleUrl。"
fi
