package installer

import (
	"context"
	"runtime"
	"strings"
)

// Run 按 spec.Role 执行内置装机；阶段日志经 log 流式回传；成功返回结构化结果。
// 首 master 回传 join 凭据与 kubeconfig；join-master / worker 回传空结果。
func Run(ctx context.Context, spec *InstallSpec, opts Options, log LogFunc) (*InstallResult, *Fail) {
	if spec == nil {
		return nil, fail(ErrStep, "缺少 InstallSpec")
	}
	if runtime.GOOS != "linux" {
		return nil, fail(ErrUnsupportedOS, "内置装机仅支持 Linux 节点")
	}
	applyDefaults(spec)
	emit(log, "开始装机 role="+spec.Role+" k8s="+spec.K8sVersion+" cni="+spec.CNI.Type)

	if f := runSteps(ctx, log, planSteps(spec, opts)); f != nil {
		return nil, f
	}

	if spec.Role == RoleFirstMaster {
		return capture(ctx, spec, log)
	}
	emit(log, "节点 "+spec.Role+" 已加入集群")
	return &InstallResult{ControlPlaneEndpoint: spec.ControlPlaneEndpoint}, nil
}

// planSteps 依角色/规格编排阶段序列。
func planSteps(spec *InstallSpec, opts Options) []Step {
	steps := []Step{
		preflightStep(spec),
		osPrepStep(),
	}
	if spec.Artifact.Mode == ArtifactOnline {
		// 在线：apt 装 containerd + 配镜像加速/pause，apt 装 kube 包；镜像由 kubeadm 在线拉取
		steps = append(steps,
			onlineContainerdStep(spec, opts),
			onlineKubePkgStep(spec, opts),
		)
	} else {
		// 离线包：下载解包制品 → 配 containerd → 导入镜像 → 装 kube 二进制
		steps = append(steps,
			artifactsStep(spec, opts),
			containerdStep(spec),
			imageImportStep(spec, opts),
			kubeBinStep(spec, opts),
		)
	}
	isMaster := spec.Role == RoleFirstMaster || spec.Role == RoleJoinMaster
	if isMaster && spec.VIP != nil && spec.VIP.Enabled {
		steps = append(steps, kubeVipStep(spec))
	}
	steps = append(steps, kubeadmStep(spec))
	if spec.Role == RoleFirstMaster {
		steps = append(steps, cniStep(spec, opts))
		if len(spec.Addons) > 0 {
			steps = append(steps, addonsStep(spec, opts))
		}
	}
	return steps
}

func applyDefaults(s *InstallSpec) {
	if s.K8sVersion == "" {
		s.K8sVersion = DefaultK8sVersion
	}
	if !strings.HasPrefix(s.K8sVersion, "v") {
		s.K8sVersion = "v" + s.K8sVersion
	}
	if s.PodCIDR == "" {
		s.PodCIDR = DefaultPodCIDR
	}
	if s.ServiceCIDR == "" {
		s.ServiceCIDR = DefaultServiceCIDR
	}
	if s.CNI == nil {
		s.CNI = &CNISpec{Type: DefaultCNIType}
	}
	if s.CNI.Type == "" {
		s.CNI.Type = DefaultCNIType
	}
	if s.CNI.Version == "" {
		if s.CNI.Type == CNICalico {
			s.CNI.Version = DefaultCalicoVer
		} else {
			s.CNI.Version = DefaultCiliumVer
		}
	}
	if s.Artifact == nil {
		s.Artifact = &ArtifactSpec{Mode: ArtifactBundle}
	}
	if s.Artifact.Mode == "" {
		s.Artifact.Mode = ArtifactBundle
	}
	// 在线 + 国内镜像：未显式指定镜像仓时，默认阿里云 google_containers（控制面镜像可拉）
	if s.Artifact.Mode == ArtifactOnline && s.Artifact.UseCNMirror && s.ImageRepository == "" {
		s.ImageRepository = cnImageRepository
	}
}
