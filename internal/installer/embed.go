package installer

import "embed"

// embeddedManifests 在线模式用的 CNI / add-on 清单，随 agent 二进制一起分发。
// 国内节点侧直连 raw.githubusercontent 拉清单不可靠，故把清单内嵌，装机时落盘再 kubectl apply；
// 清单内引用的镜像走 containerd registry mirror（daocloud）+ 阿里云镜像仓在线拉取。
//
// 版本（与 scripts/build-k8s-bundle.sh 默认一致，升级时同步替换文件并改这里的常量）：
//
//	calico         v3.29.1
//	ingress-nginx  controller-v1.11.3（baremetal / NodePort）
//	metrics-server v0.7.2（已注入 --kubelet-insecure-tls）
//	cert-manager   v1.16.2
//
//go:embed manifests/calico.yaml manifests/addons/*.yaml
var embeddedManifests embed.FS

// embeddedCNIManifest 返回内嵌 CNI 清单内容（在线模式仅支持 calico）。
func embeddedCNIManifest(cniType string) ([]byte, bool) {
	if cniType != CNICalico {
		return nil, false
	}
	b, err := embeddedManifests.ReadFile("manifests/calico.yaml")
	if err != nil {
		return nil, false
	}
	return b, true
}

// embeddedAddonManifest 返回内嵌 add-on 清单内容（ingress-nginx/metrics-server/cert-manager）。
func embeddedAddonManifest(name string) ([]byte, bool) {
	b, err := embeddedManifests.ReadFile("manifests/addons/" + name + ".yaml")
	if err != nil {
		return nil, false
	}
	return b, true
}
