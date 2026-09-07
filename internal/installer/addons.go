package installer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// addonsStep 仅在首 master、CNI 之后执行：按 spec.Addons 顺序 apply 离线包 addons/ 下的清单。
// 每个 add-on 对应 addons/<name>.yaml（单文件）或 addons/<name>/*.yaml（多文件，按名排序）。
// 镜像走统一的 imageImportStep（离线包 images/ 里已含 add-on 镜像），此处只 apply 清单。
func addonsStep(spec *InstallSpec, opts Options) Step {
	return Step{
		Name: "集群 add-on (" + strings.Join(spec.Addons, ",") + ")",
		Run: func(ctx context.Context, log LogFunc) *Fail {
			// add-on 都是集群级资源，走 admin.conf
			_ = os.Setenv("KUBECONFIG", adminConf)
			online := spec.Artifact != nil && spec.Artifact.Mode == ArtifactOnline
			base := filepath.Join(bundleDir(opts), "addons")
			for _, raw := range spec.Addons {
				name := strings.TrimSpace(raw)
				if name == "" {
					continue
				}
				var files []string
				var f *Fail
				if online {
					files, f = embeddedAddonToDisk(name, opts, log)
				} else {
					files, f = addonManifests(base, name)
				}
				if f != nil {
					return f
				}
				emit(log, "apply add-on: "+name+"（"+strconv.Itoa(len(files))+" 个清单）")
				for _, file := range files {
					// server-side apply：cert-manager 等 CRD 体积大，client-side 会触碰 annotation 长度上限
					if fx := mustRun(ctx, log, "kubectl", "apply", "--server-side", "--force-conflicts", "-f", file); fx != nil {
						return fx
					}
				}
			}
			return nil
		},
	}
}

// embeddedAddonToDisk 在线模式：把内嵌 add-on 清单落盘，返回文件路径供 kubectl apply。
func embeddedAddonToDisk(name string, opts Options, log LogFunc) ([]string, *Fail) {
	content, ok := embeddedAddonManifest(name)
	if !ok {
		return nil, failf(ErrStep, "在线模式未内置 add-on 清单 %s（支持 ingress-nginx/metrics-server/cert-manager）", name)
	}
	dst := filepath.Join(opts.DataDir, "addon-"+name+".yaml")
	if f := writeFile(dst, string(content), log); f != nil {
		return nil, f
	}
	return []string{dst}, nil
}

// addonManifests 解析某 add-on 的清单文件：优先单文件 addons/<name>.yaml，否则目录 addons/<name>/*.yaml。
func addonManifests(base, name string) ([]string, *Fail) {
	single := filepath.Join(base, name+".yaml")
	if fileExists(single) {
		return []string{single}, nil
	}
	dir := filepath.Join(base, name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, failf(ErrStep, "离线包缺少 add-on 清单 addons/%s(.yaml)——请用带该 add-on 的离线包重建", name)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml")) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, failf(ErrStep, "add-on %s 清单为空", name)
	}
	return files, nil
}
