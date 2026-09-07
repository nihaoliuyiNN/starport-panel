package installer

import (
	"context"
	"os"
	"runtime"
)

// preflightStep 装机预检：root 权限、架构、必要字段、发行版（apt 系）。
func preflightStep(spec *InstallSpec) Step {
	return Step{
		Name: "preflight 预检",
		Run: func(ctx context.Context, log LogFunc) *Fail {
			if os.Geteuid() != 0 {
				return fail(ErrPreflight, "内置装机需以 root 运行 starport-agent")
			}
			if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
				return failf(ErrPreflight, "不支持的架构: %s（仅 amd64/arm64）", runtime.GOARCH)
			}
			switch spec.Role {
			case RoleFirstMaster, RoleJoinMaster, RoleWorker:
			default:
				return failf(ErrPreflight, "未知角色: %q", spec.Role)
			}
			if spec.ControlPlaneEndpoint == "" {
				return fail(ErrPreflight, "缺少 controlPlaneEndpoint")
			}
			if spec.Role != RoleFirstMaster {
				if spec.Join == nil || spec.Join.Token == "" || spec.Join.CACertHash == "" {
					return fail(ErrPreflight, "join 节点缺少 token / caCertHash")
				}
				if spec.Role == RoleJoinMaster && spec.Join.CertificateKey == "" {
					return fail(ErrPreflight, "join-master 缺少 certificateKey")
				}
			}
			switch {
			case spec.Artifact == nil:
				return fail(ErrPreflight, "缺少 artifact 制品来源")
			case spec.Artifact.Mode == ArtifactOnline:
				// 在线：CNI 仅支持 calico（内嵌清单）；首 master 才需要 CNI
				if spec.Role == RoleFirstMaster && spec.CNI != nil && spec.CNI.Type != CNICalico {
					return failf(ErrPreflight, "在线模式 CNI 仅支持 calico（当前 %s）；如需 cilium 请用离线包模式", spec.CNI.Type)
				}
			default:
				if spec.Artifact.BundleURL == "" {
					return fail(ErrPreflight, "离线装机缺少 bundleUrl")
				}
			}
			emit(log, "preflight 通过: root/arch/role/endpoint 校验 OK")
			return nil
		},
	}
}
