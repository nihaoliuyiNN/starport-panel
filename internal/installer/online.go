package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// online.go 在线制品来源：节点有外网（NAT）时，二进制走 apt（默认阿里云镜像）、镜像走
// registry.aliyuncs.com + containerd registry mirror（daocloud），免托管大离线包。
// 这里复用退役 server/internal/k8sinstall 在国内环境实测打磨过的 bash（apt 源探测、pause
// sandbox、registry mirror），以 bash 执行——纯 Go 重写这套 apt/containerd 细节收益低、易踩坑。

// runBash 把 bash 内容落到 DataDir 下的临时脚本并流式执行。
func runBash(ctx context.Context, opts Options, log LogFunc, name, content string) *Fail {
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return failf(ErrStep, "建目录 %s 失败: %v", opts.DataDir, err)
	}
	p := filepath.Join(opts.DataDir, name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		return failf(ErrStep, "写脚本 %s 失败: %v", p, err)
	}
	return mustRun(ctx, log, "bash", p)
}

// onlineContainerdStep 在线装并配置 containerd：apt 安装、SystemdCgroup、（国内）registry mirror
// 与 pause sandbox 指向阿里云。幂等由脚本内部处理。
func onlineContainerdStep(spec *InstallSpec, opts Options) Step {
	return Step{
		Name: "containerd 在线安装与配置",
		Skip: func(ctx context.Context) bool {
			return serviceActive("containerd") && fileContains(containerdConfig, "SystemdCgroup = true")
		},
		Run: func(ctx context.Context, log LogFunc) *Fail {
			return runBash(ctx, opts, log, "online-containerd.sh", onlineContainerdScript(spec))
		},
	}
}

// onlineKubePkgStep 在线 apt 钉版本安装 kubeadm/kubelet/kubectl（国内默认阿里云 kubernetes 镜像）。
func onlineKubePkgStep(spec *InstallSpec, opts Options) Step {
	return Step{
		Name: "kube 包在线安装 (kubeadm/kubelet/kubectl)",
		Skip: func(ctx context.Context) bool {
			return binExists("kubeadm") && binExists("kubelet") && binExists("kubectl")
		},
		Run: func(ctx context.Context, log LogFunc) *Fail {
			return runBash(ctx, opts, log, "online-kube-pkg.sh", onlineKubePkgScript(spec))
		},
	}
}

// pauseImage 在线模式的 pause 镜像（国内走阿里云，否则官方）。
func pauseImage(spec *InstallSpec) string {
	repo := "registry.k8s.io"
	if spec.Artifact != nil && spec.Artifact.UseCNMirror {
		repo = cnImageRepository
	}
	return strings.TrimRight(repo, "/") + "/pause:" + DefaultPauseTag
}

func onlineContainerdScript(spec *InstallSpec) string {
	cn := spec.Artifact != nil && spec.Artifact.UseCNMirror
	pause := pauseImage(spec)
	return `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
echo "==> [online containerd] 安装"
if command -v apt-get >/dev/null 2>&1; then
  apt-get update -y
  apt-get install -y containerd || apt-get install -y containerd.io || true
  apt-get install -y cri-tools || true
elif command -v yum >/dev/null 2>&1; then
  yum install -y containerd || true
fi
mkdir -p /etc/containerd
containerd config default >/etc/containerd/config.toml 2>/dev/null || true
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml || true
` + onlineRegistryMirrorsBash(cn) + `
# sandbox(pause) 指向可拉取的镜像，避免 kubelet 去拉 registry.k8s.io/pause 超时
sed -i "s|sandbox_image = \".*\"|sandbox_image = \"` + pause + `\"|" /etc/containerd/config.toml || true
sed -i "s|sandbox_image = '.*'|sandbox_image = '` + pause + `'|" /etc/containerd/config.toml || true
sed -i "s|sandbox = \".*\"|sandbox = \"` + pause + `\"|" /etc/containerd/config.toml || true
if ! grep -qE 'sandbox(_image)?\s*=' /etc/containerd/config.toml; then
  printf '\n[plugins."io.containerd.grpc.v1.cri"]\n  sandbox_image = "` + pause + `"\n' >>/etc/containerd/config.toml
fi
grep -nE 'sandbox' /etc/containerd/config.toml || true
systemctl enable containerd
systemctl restart containerd
sleep 2
systemctl is-active containerd
# 预拉 pause，join/init 前就绪
CTR="ctr -a /run/containerd/containerd.sock"
[ -x /usr/bin/ctr ] && CTR="/usr/bin/ctr -a /run/containerd/containerd.sock"
$CTR -n k8s.io images pull "` + pause + `" >/dev/null 2>&1 || $CTR -n k8s.io images pull --hosts-dir /etc/containerd/certs.d "` + pause + `" || true
echo "==> [online containerd] done"
`
}

// onlineRegistryMirrorsBash 给 containerd 配 pull-through 镜像加速（certs.d/hosts.toml，走 daocloud）。
// 国内节点直连 registry.k8s.io / docker.io / quay.io 必然超时；CNI/addon 镜像多按 digest 钉死，
// 改不动镜像名，只能让 containerd 自己走代理拉（透传，digest 一致）。移植自退役 server。
func onlineRegistryMirrorsBash(cn bool) string {
	if !cn {
		return ""
	}
	return `
echo "==> [online containerd] 配 registry mirror (daocloud)"
CERTS_D=/etc/containerd/certs.d
mkdir -p "$CERTS_D"
write_hosts() {
  local host="$1"; shift
  local m
  mkdir -p "${CERTS_D}/${host}"
  {
    echo "server = \"https://${host}\""
    for m in "$@"; do
      echo ""
      echo "[host.\"https://${m}\"]"
      echo "  capabilities = [\"pull\", \"resolve\"]"
    done
  } >"${CERTS_D}/${host}/hosts.toml"
}
write_hosts registry.k8s.io k8s.m.daocloud.io
write_hosts k8s.gcr.io k8s-gcr.m.daocloud.io
write_hosts gcr.io gcr.m.daocloud.io
write_hosts docker.io docker.m.daocloud.io
write_hosts quay.io quay.m.daocloud.io
write_hosts ghcr.io ghcr.m.daocloud.io
# containerd 2.x 默认已带 config_path=''，重复追加 section 会让 TOML 解析失败起不来：有则原地改、无则按大版本追加
if grep -qE '^[[:space:]]*config_path[[:space:]]*=' /etc/containerd/config.toml; then
  sed -i -E "s|^([[:space:]]*)config_path[[:space:]]*=.*|\1config_path = \"${CERTS_D}\"|" /etc/containerd/config.toml
else
  CD_MAJOR=$(containerd --version 2>/dev/null | awk '{print $3}' | tr -d 'v' | cut -d. -f1)
  if [ "${CD_MAJOR:-1}" -ge 2 ]; then
    printf '\n[plugins."io.containerd.cri.v1.images".registry]\n  config_path = "%s"\n' "$CERTS_D" >>/etc/containerd/config.toml
  else
    printf '\n[plugins."io.containerd.grpc.v1.cri".registry]\n  config_path = "%s"\n' "$CERTS_D" >>/etc/containerd/config.toml
  fi
fi
grep -nE 'config_path' /etc/containerd/config.toml || true
`
}

// onlineKubePkgScript apt 钉版本安装 kubeadm/kubelet/kubectl。
// 官方源路径含冒号：pkgs.k8s.io/core:/stable:/v1.35/deb/
// 阿里云镜像无冒号：mirrors.aliyun.com/kubernetes-new/core/stable/v1.35/deb/
// 移植自退役 server/internal/k8sinstall.BuildInstallKubePackages。
func onlineKubePkgScript(spec *InstallSpec) string {
	ver := strings.TrimPrefix(spec.K8sVersion, "v")
	series := minorSeries(spec.K8sVersion)
	official := fmt.Sprintf("https://pkgs.k8s.io/core:/stable:/%s/deb/", series)
	aliyun := fmt.Sprintf("https://mirrors.aliyun.com/kubernetes-new/core/stable/%s/deb/", series)
	primary, fallback := official, aliyun
	if spec.Artifact != nil && spec.Artifact.UseCNMirror {
		primary, fallback = aliyun, official
	}
	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
echo "==> [online kube-pkg] %s (series %s)"
VER="%s"
PRIMARY="%s"
FALLBACK="%s"
pick_repo() {
  local base="$1"
  # 用 GET 探测（部分镜像对 HEAD 返回 404）
  if curl -fsSL "${base}Release.key" -o /tmp/k8s-Release.key 2>/dev/null; then
    echo "$base"; return 0
  fi
  return 1
}
if ! command -v apt-get >/dev/null 2>&1; then
  echo "ERROR: 在线模式目前仅支持 apt 系（Ubuntu/Debian）" >&2; exit 1
fi
apt-get update -y || true
apt-get install -y apt-transport-https ca-certificates curl gpg
mkdir -p /etc/apt/keyrings
REPO=""
if pick_repo "$PRIMARY"; then REPO="$PRIMARY"; elif pick_repo "$FALLBACK"; then REPO="$FALLBACK"; fi
if [ -z "$REPO" ]; then
  echo "ERROR: 无法从 $PRIMARY 或 $FALLBACK 获取 Release.key（节点需能访问其一）" >&2; exit 1
fi
echo "==> apt repo: $REPO"
rm -f /etc/apt/keyrings/kubernetes-apt-keyring.gpg
gpg --batch --yes --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg /tmp/k8s-Release.key
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] ${REPO} /" >/etc/apt/sources.list.d/kubernetes.list
apt-get update -y
if apt-cache madison kubeadm | awk '{print $3}' | grep -q "^${VER}-"; then
  apt-get install -y "kubelet=${VER}-*" "kubeadm=${VER}-*" "kubectl=${VER}-*"
else
  echo "WARN: ${VER} 不在仓库，安装该 series 最新"
  apt-get install -y kubelet kubeadm kubectl
fi
apt-mark hold kubelet kubeadm kubectl || true
systemctl enable kubelet
kubeadm version
echo "==> [online kube-pkg] done"
`, spec.K8sVersion, series, ver, primary, fallback)
}

// minorSeries 从 v1.35.7 / 1.35.7 得到仓库目录用的 v1.35。
func minorSeries(k8sVersion string) string {
	v := strings.TrimPrefix(strings.TrimSpace(k8sVersion), "v")
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return "v" + parts[0] + "." + parts[1]
	}
	return "v1.35"
}
