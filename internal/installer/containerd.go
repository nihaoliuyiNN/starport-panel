package installer

import (
	"context"
	"regexp"
	"strings"
)

const containerdConfig = "/etc/containerd/config.toml"

// containerdStep 配置并启用 containerd：SystemdCgroup=true、sandbox_image 对齐、启用并重启。
// 二进制与 systemd 单元由 artifacts 阶段从离线包落地，这里只做配置与启用。
func containerdStep(spec *InstallSpec) Step {
	return Step{
		Name: "containerd 配置与启用",
		Skip: func(ctx context.Context) bool {
			return serviceActive("containerd") && fileContains(containerdConfig, "SystemdCgroup = true")
		},
		Run: func(ctx context.Context, log LogFunc) *Fail {
			if !binExists("containerd") {
				return fail(ErrStep, "未找到 containerd 二进制（请确认离线包 bin/ 含 containerd）")
			}

			out, err := output(ctx, "containerd", "config", "default")
			if err != nil {
				return failf(ErrStep, "生成 containerd 默认配置失败: %v", err)
			}
			cfg := patchContainerdConfig(out, spec)
			if f := writeFile(containerdConfig, cfg, log); f != nil {
				return f
			}

			if f := mustRun(ctx, log, "systemctl", "enable", "containerd"); f != nil {
				return f
			}
			if f := mustRun(ctx, log, "systemctl", "restart", "containerd"); f != nil {
				return f
			}
			return nil
		},
	}
}

var reSandboxImage = regexp.MustCompile(`(?m)^(\s*)sandbox_image\s*=\s*".*"`)

// patchContainerdConfig 把 SystemdCgroup 置 true，并将 sandbox_image 对齐到目标 pause 镜像。
func patchContainerdConfig(cfg string, spec *InstallSpec) string {
	cfg = strings.ReplaceAll(cfg, "SystemdCgroup = false", "SystemdCgroup = true")

	repo := spec.ImageRepository
	if repo == "" {
		repo = "registry.k8s.io"
	}
	pause := "sandbox_image = \"" + strings.TrimRight(repo, "/") + "/pause:" + DefaultPauseTag + "\""
	if reSandboxImage.MatchString(cfg) {
		cfg = reSandboxImage.ReplaceAllString(cfg, "${1}"+pause)
	}
	return cfg
}
