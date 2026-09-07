package installer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cniStep 仅在首 master 执行：
//   - bundle : apply 离线包 cni/ 目录下的全部清单（Cilium/Calico 由构建离线包时选定）。
//   - online : apply agent 内嵌的 calico 清单（镜像走 containerd registry mirror 在线拉取）。
func cniStep(spec *InstallSpec, opts Options) Step {
	return Step{
		Name: "CNI 网络插件 (" + spec.CNI.Type + ")",
		Run: func(ctx context.Context, log LogFunc) *Fail {
			_ = os.Setenv("KUBECONFIG", adminConf)
			if spec.Artifact != nil && spec.Artifact.Mode == ArtifactOnline {
				return applyEmbeddedCNI(ctx, spec, opts, log)
			}
			return applyBundleCNI(ctx, opts, log)
		},
	}
}

// applyEmbeddedCNI 在线模式：把内嵌 CNI 清单落盘并 apply（仅 calico）。
func applyEmbeddedCNI(ctx context.Context, spec *InstallSpec, opts Options, log LogFunc) *Fail {
	content, ok := embeddedCNIManifest(spec.CNI.Type)
	if !ok {
		return failf(ErrStep, "在线模式仅内置 calico CNI 清单（当前 %s）", spec.CNI.Type)
	}
	dst := filepath.Join(opts.DataDir, "cni-"+spec.CNI.Type+".yaml")
	if f := writeFile(dst, string(content), log); f != nil {
		return f
	}
	emit(log, "apply 内嵌 CNI 清单: "+spec.CNI.Type)
	return mustRun(ctx, log, "kubectl", "apply", "-f", dst)
}

// applyBundleCNI apply 离线包 cni/ 目录下的全部 yaml。
func applyBundleCNI(ctx context.Context, opts Options, log LogFunc) *Fail {
	dir := filepath.Join(bundleDir(opts), "cni")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return failf(ErrStep, "离线包缺少 cni 目录: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml")) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return fail(ErrStep, "离线包 cni 目录无清单文件")
	}
	for _, f := range files {
		if fx := mustRun(ctx, log, "kubectl", "apply", "-f", f); fx != nil {
			return fx
		}
	}
	return nil
}
