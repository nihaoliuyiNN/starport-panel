package installer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// bundleDir 离线包在本机的解包目录。
func bundleDir(opts Options) string { return filepath.Join(opts.DataDir, "k8s-bundle") }

// artifactsStep 制品准备：
//   - online   : 无需动作（apt 源 + kubeadm 拉镜像）。
//   - registry : 无需动作（kubeadm 用 imageRepository 从私有仓拉）。
//   - bundle   : 下载控制面托管的离线包并解包；若包内含 containerd 二进制则一并落地。
func artifactsStep(spec *InstallSpec, opts Options) Step {
	return Step{
		Name: "artifacts 制品准备",
		Skip: func(ctx context.Context) bool {
			return spec.Artifact.Mode != ArtifactBundle
		},
		Run: func(ctx context.Context, log LogFunc) *Fail {
			if spec.Artifact.BundleURL == "" {
				return fail(ErrStep, "bundle 模式缺少 bundleUrl")
			}
			dir := bundleDir(opts)
			_ = os.RemoveAll(dir)
			archive := filepath.Join(opts.DataDir, "k8s-bundle.tar.gz")
			emit(log, "下载离线包: "+spec.Artifact.BundleURL)
			if err := fetchBundle(ctx, spec.Artifact.BundleURL, archive, log); err != nil {
				return failf(ErrStep, "下载离线包失败: %v", err)
			}
			if err := extractTarGz(archive, dir); err != nil {
				return failf(ErrStep, "解包离线包失败: %v", err)
			}
			emit(log, "离线包已解包到 "+dir)

			// 包内若含 containerd（bin/containerd*），落到 /usr/local/bin
			binDir := filepath.Join(dir, "bin")
			for _, name := range []string{"containerd", "containerd-shim-runc-v2", "runc", "ctr"} {
				src := filepath.Join(binDir, name)
				if fileExists(src) {
					if f := installBinary(src, "/usr/local/bin/"+name, log); f != nil {
						return f
					}
				}
			}
			// containerd systemd 单元（bundle 提供则装，供 containerd 阶段 enable/restart）
			svc := filepath.Join(dir, "systemd", "containerd.service")
			if fileExists(svc) {
				if f := copyFileTo(svc, "/usr/lib/systemd/system/containerd.service", log); f != nil {
					return f
				}
				if f := mustRun(ctx, log, "systemctl", "daemon-reload"); f != nil {
					return f
				}
			}
			// CNI 插件二进制（loopback/bridge/portmap 等）→ /opt/cni/bin
			cniBin := filepath.Join(binDir, "cni")
			if plugins, err := os.ReadDir(cniBin); err == nil {
				for _, p := range plugins {
					if p.IsDir() {
						continue
					}
					if f := installBinary(filepath.Join(cniBin, p.Name()), "/opt/cni/bin/"+p.Name(), log); f != nil {
						return f
					}
				}
			}
			return nil
		},
	}
}

// imageImportStep 离线镜像导入：把 bundle 内 images/*.tar 通过 ctr 导入 k8s.io 命名空间。
// 必须在 containerd 启动之后执行。
func imageImportStep(spec *InstallSpec, opts Options) Step {
	return Step{
		Name: "images 离线镜像导入",
		Skip: func(ctx context.Context) bool {
			return spec.Artifact.Mode != ArtifactBundle
		},
		Run: func(ctx context.Context, log LogFunc) *Fail {
			imagesDir := filepath.Join(bundleDir(opts), "images")
			entries, err := os.ReadDir(imagesDir)
			if err != nil {
				emit(log, "warn: 无 images 目录，跳过镜像导入")
				return nil
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar") {
					continue
				}
				p := filepath.Join(imagesDir, e.Name())
				if f := mustRun(ctx, log, "ctr", "-n", "k8s.io", "images", "import", p); f != nil {
					return f
				}
			}
			return nil
		},
	}
}

// bundleInstallKube 从离线包安装 kubelet/kubeadm/kubectl 及 kubelet systemd 单元。
func bundleInstallKube(ctx context.Context, opts Options, log LogFunc) *Fail {
	binDir := filepath.Join(bundleDir(opts), "bin")
	for _, name := range []string{"kubeadm", "kubelet", "kubectl"} {
		src := filepath.Join(binDir, name)
		if !fileExists(src) {
			return failf(ErrStep, "离线包缺少二进制 bin/%s", name)
		}
		if f := installBinary(src, "/usr/local/bin/"+name, log); f != nil {
			return f
		}
	}

	// kubelet systemd 单元（若包内提供则用之，否则写内置模板）
	unitSrc := filepath.Join(bundleDir(opts), "systemd", "kubelet.service")
	if fileExists(unitSrc) {
		if f := copyFileTo(unitSrc, "/usr/lib/systemd/system/kubelet.service", log); f != nil {
			return f
		}
	} else if f := writeFile("/usr/lib/systemd/system/kubelet.service", kubeletUnit, log); f != nil {
		return f
	}
	dropinSrc := filepath.Join(bundleDir(opts), "systemd", "10-kubeadm.conf")
	if fileExists(dropinSrc) {
		if f := copyFileTo(dropinSrc, "/usr/lib/systemd/system/kubelet.service.d/10-kubeadm.conf", log); f != nil {
			return f
		}
	} else if f := writeFile("/usr/lib/systemd/system/kubelet.service.d/10-kubeadm.conf", kubeadmDropin, log); f != nil {
		return f
	}

	if f := mustRun(ctx, log, "systemctl", "daemon-reload"); f != nil {
		return f
	}
	if f := mustRun(ctx, log, "systemctl", "enable", "--now", "kubelet"); f != nil {
		return f
	}
	return nil
}

// installBinary 复制并 chmod 0755（用于可执行文件）。
func installBinary(src, dst string, log LogFunc) *Fail {
	if f := copyFileTo(src, dst, log); f != nil {
		return f
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return failf(ErrStep, "chmod %s 失败: %v", dst, err)
	}
	return nil
}

func copyFileTo(src, dst string, log LogFunc) *Fail {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return failf(ErrStep, "建目录 %s 失败: %v", filepath.Dir(dst), err)
	}
	in, err := os.Open(src)
	if err != nil {
		return failf(ErrStep, "打开 %s 失败: %v", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return failf(ErrStep, "创建 %s 失败: %v", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return failf(ErrStep, "复制到 %s 失败: %v", dst, err)
	}
	emit(log, "安装 "+dst)
	return nil
}

// bundleManifest 分卷清单（对齐 build-k8s-bundle.sh --split 产出的 <archive>.parts.json）。
// parts 为各分卷文件名（basename，与清单同目录），按数组顺序拼接即还原原 tar.gz。
type bundleManifest struct {
	Archive string   `json:"archive"`
	Sha256  string   `json:"sha256"`
	Size    int64    `json:"size"`
	Parts   []string `json:"parts"`
}

// fetchBundle 下载离线包：URL 以 .parts.json 结尾按分卷清单处理（下载各卷→顺序拼接→sha256 校验），
// 否则按单文件直接下载。分卷用于绕开 Gitee 等平台的单附件大小上限。
func fetchBundle(ctx context.Context, url, archive string, log LogFunc) error {
	if strings.HasSuffix(url, ".parts.json") {
		return fetchSplitBundle(ctx, url, archive, log)
	}
	return httpDownload(ctx, url, archive)
}

// fetchSplitBundle 按清单把各分卷下载并顺序拼成 archive；清单带 sha256 时合并后校验。
func fetchSplitBundle(ctx context.Context, manifestURL, archive string, log LogFunc) error {
	mf := archive + ".parts.json"
	if err := httpDownload(ctx, manifestURL, mf); err != nil {
		return fmt.Errorf("下载分卷清单失败: %w", err)
	}
	raw, err := os.ReadFile(mf)
	if err != nil {
		return fmt.Errorf("读分卷清单失败: %w", err)
	}
	var m bundleManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("解析分卷清单失败: %w", err)
	}
	if len(m.Parts) == 0 {
		return fmt.Errorf("分卷清单未列出任何分卷")
	}
	// 分卷 URL 相对清单所在目录解析
	base := manifestURL
	if i := strings.LastIndex(manifestURL, "/"); i >= 0 {
		base = manifestURL[:i+1]
	}
	out, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer out.Close()
	h := sha256.New()
	part := archive + ".part"
	for i, name := range m.Parts {
		emit(log, fmt.Sprintf("下载分卷 %d/%d: %s", i+1, len(m.Parts), name))
		if err := httpDownload(ctx, base+name, part); err != nil {
			return fmt.Errorf("下载分卷 %s 失败: %w", name, err)
		}
		pf, err := os.Open(part)
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.MultiWriter(out, h), pf); err != nil {
			pf.Close()
			return err
		}
		pf.Close()
		_ = os.Remove(part)
	}
	if m.Sha256 != "" {
		sum := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(sum, m.Sha256) {
			return fmt.Errorf("分卷合并后 sha256 校验失败: 期望 %s 实得 %s", m.Sha256, sum)
		}
		emit(log, "分卷合并校验通过 sha256="+sum)
	}
	return nil
}

// extractTarGz 解压 .tar.gz 到 dest（防目录穿越）。
func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && target != filepath.Clean(dest) {
			continue // 跳过越界路径
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

const kubeletUnit = `[Unit]
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
`

const kubeadmDropin = `[Service]
Environment="KUBELET_KUBECONFIG_ARGS=--bootstrap-kubeconfig=/etc/kubernetes/bootstrap-kubelet.conf --kubeconfig=/etc/kubernetes/kubelet.conf"
Environment="KUBELET_CONFIG_ARGS=--config=/var/lib/kubelet/config.yaml"
EnvironmentFile=-/var/lib/kubelet/kubeadm-flags.env
EnvironmentFile=-/etc/default/kubelet
ExecStart=
ExecStart=/usr/local/bin/kubelet $KUBELET_KUBECONFIG_ARGS $KUBELET_CONFIG_ARGS $KUBELET_KUBEADM_ARGS $KUBELET_EXTRA_ARGS
`
