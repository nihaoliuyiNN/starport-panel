package installer

import "context"

// kubeBinStep 从离线包安装 kubelet/kubeadm/kubectl 及 kubelet systemd 单元并启用。
func kubeBinStep(spec *InstallSpec, opts Options) Step {
	return Step{
		Name: "kube 二进制 (kubelet/kubeadm/kubectl)",
		Skip: func(ctx context.Context) bool {
			return binExists("kubeadm") && binExists("kubelet") && binExists("kubectl")
		},
		Run: func(ctx context.Context, log LogFunc) *Fail {
			return bundleInstallKube(ctx, opts, log)
		},
	}
}
