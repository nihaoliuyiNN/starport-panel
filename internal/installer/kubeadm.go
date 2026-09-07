package installer

import (
	"context"
	"strings"
)

const kubeadmInitConfig = "/etc/kubernetes/kubeadm-init.yaml"

// kubeadmStep 按角色执行 kubeadm：
//   - first-master: 生成 v1beta4 配置并 kubeadm init --upload-certs
//   - join-master : kubeadm join --control-plane --certificate-key
//   - worker      : kubeadm join
//
// 幂等：已存在 kubelet.conf（已加入/已初始化）则跳过。
func kubeadmStep(spec *InstallSpec) Step {
	return Step{
		Name: "kubeadm " + spec.Role,
		Skip: func(ctx context.Context) bool { return fileExists(kubeletConf) },
		Run: func(ctx context.Context, log LogFunc) *Fail {
			switch spec.Role {
			case RoleFirstMaster:
				return kubeadmInit(ctx, spec, log)
			case RoleJoinMaster, RoleWorker:
				return kubeadmJoin(ctx, spec, log)
			default:
				return failf(ErrStep, "未知角色: %q", spec.Role)
			}
		},
	}
}

func kubeadmInit(ctx context.Context, spec *InstallSpec, log LogFunc) *Fail {
	if f := writeFile(kubeadmInitConfig, renderInitConfig(spec), log); f != nil {
		return f
	}
	// 控制面镜像已由 imageImport 阶段从离线包导入，直接 init（不联网拉镜像）
	return mustRun(ctx, log, "kubeadm", "init",
		"--config", kubeadmInitConfig,
		"--upload-certs",
		"--ignore-preflight-errors=all")
}

func kubeadmJoin(ctx context.Context, spec *InstallSpec, log LogFunc) *Fail {
	args := []string{"join", spec.ControlPlaneEndpoint,
		"--token", spec.Join.Token,
		"--discovery-token-ca-cert-hash", spec.Join.CACertHash,
		"--ignore-preflight-errors=all",
	}
	if spec.Role == RoleJoinMaster {
		args = append(args, "--control-plane", "--certificate-key", spec.Join.CertificateKey)
		if spec.AdvertiseAddress != "" {
			args = append(args, "--apiserver-advertise-address", spec.AdvertiseAddress)
		}
	}
	return mustRun(ctx, log, "kubeadm", args...)
}

// renderInitConfig 生成 kubeadm v1beta4 的 Init+Cluster 配置。
func renderInitConfig(spec *InstallSpec) string {
	var b strings.Builder
	b.WriteString("apiVersion: kubeadm.k8s.io/v1beta4\n")
	b.WriteString("kind: InitConfiguration\n")
	b.WriteString("localAPIEndpoint:\n")
	if spec.AdvertiseAddress != "" {
		b.WriteString("  advertiseAddress: " + spec.AdvertiseAddress + "\n")
	}
	b.WriteString("  bindPort: 6443\n")
	b.WriteString("nodeRegistration:\n")
	b.WriteString("  criSocket: unix:///run/containerd/containerd.sock\n")
	b.WriteString("---\n")
	b.WriteString("apiVersion: kubeadm.k8s.io/v1beta4\n")
	b.WriteString("kind: ClusterConfiguration\n")
	b.WriteString("kubernetesVersion: " + spec.K8sVersion + "\n")
	b.WriteString("controlPlaneEndpoint: " + spec.ControlPlaneEndpoint + "\n")
	if spec.ImageRepository != "" {
		b.WriteString("imageRepository: " + spec.ImageRepository + "\n")
	}
	b.WriteString("networking:\n")
	b.WriteString("  podSubnet: " + spec.PodCIDR + "\n")
	b.WriteString("  serviceSubnet: " + spec.ServiceCIDR + "\n")

	sans := collectCertSANs(spec)
	if len(sans) > 0 {
		b.WriteString("apiServer:\n")
		b.WriteString("  certSANs:\n")
		for _, s := range sans {
			b.WriteString("    - \"" + s + "\"\n")
		}
	}
	return b.String()
}

// collectCertSANs 合并 VIP、endpoint host、通告 IP 与显式 certSANs，去重。
func collectCertSANs(spec *InstallSpec) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if spec.VIP != nil && spec.VIP.Address != "" {
		add(spec.VIP.Address)
	}
	if h := hostOf(spec.ControlPlaneEndpoint); h != "" {
		add(h)
	}
	add(spec.AdvertiseAddress)
	for _, s := range spec.CertSANs {
		add(s)
	}
	return out
}

// hostOf 从 host:port 取 host。
func hostOf(endpoint string) string {
	if i := strings.LastIndex(endpoint, ":"); i > 0 {
		return endpoint[:i]
	}
	return endpoint
}
