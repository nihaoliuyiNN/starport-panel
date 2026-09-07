package installer

import (
	"context"
	"net"
	"strings"
)

const defaultKubeVipVersion = "v0.8.7"

// kubeVipStep 在控制面节点写入 kube-vip 静态 Pod（ARP/L2 模式），为控制面提供高可用 VIP。
// 写到 /etc/kubernetes/manifests 后，kubelet 起来即拉起；首 master 在 kubeadm init 前放置，
// 使 controlPlaneEndpoint(VIP:6443) 在 init 阶段即可被 ARP 广播接管。
func kubeVipStep(spec *InstallSpec) Step {
	return Step{
		Name: "kube-vip 控制面 VIP",
		Skip: func(ctx context.Context) bool {
			return fileExists(kubeManifestDir + "/kube-vip.yaml")
		},
		Run: func(ctx context.Context, log LogFunc) *Fail {
			iface := spec.VIP.Interface
			if iface == "" {
				iface = detectDefaultIface(spec.AdvertiseAddress)
			}
			if iface == "" {
				return fail(ErrStep, "无法探测 VIP 绑定网卡，请在 VIPSpec.interface 指定")
			}
			ver := spec.VIP.Version
			if ver == "" {
				ver = defaultKubeVipVersion
			}
			manifest := renderKubeVip(spec.VIP.Address, iface, ver)
			return writeFile(kubeManifestDir+"/kube-vip.yaml", manifest, log)
		},
	}
}

// detectDefaultIface 依据本节点通告 IP 找到对应网卡名；无法匹配时返回首个非回环 up 网卡。
func detectDefaultIface(advertiseIP string) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var fallback string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			if fallback == "" {
				fallback = ifc.Name
			}
			if advertiseIP != "" && ipnet.IP.String() == advertiseIP {
				return ifc.Name
			}
		}
	}
	return fallback
}

// renderKubeVip 生成 kube-vip 静态 Pod 清单（ARP + 控制面 + leaderElection）。
func renderKubeVip(vip, iface, version string) string {
	r := strings.NewReplacer(
		"__VIP__", vip,
		"__IFACE__", iface,
		"__VERSION__", version,
	)
	return r.Replace(kubeVipTemplate)
}

const kubeVipTemplate = `apiVersion: v1
kind: Pod
metadata:
  name: kube-vip
  namespace: kube-system
spec:
  containers:
    - name: kube-vip
      image: ghcr.io/kube-vip/kube-vip:__VERSION__
      imagePullPolicy: IfNotPresent
      args: ["manager"]
      env:
        - name: vip_arp
          value: "true"
        - name: port
          value: "6443"
        - name: vip_interface
          value: "__IFACE__"
        - name: vip_cidr
          value: "32"
        - name: cp_enable
          value: "true"
        - name: cp_namespace
          value: "kube-system"
        - name: vip_ddns
          value: "false"
        - name: vip_leaderelection
          value: "true"
        - name: vip_leaseduration
          value: "5"
        - name: vip_renewdeadline
          value: "3"
        - name: vip_retryperiod
          value: "1"
        - name: address
          value: "__VIP__"
      securityContext:
        capabilities:
          add: ["NET_ADMIN", "NET_RAW"]
      volumeMounts:
        - mountPath: /etc/kubernetes/admin.conf
          name: kubeconfig
  hostAliases:
    - hostnames: ["kubernetes"]
      ip: 127.0.0.1
  hostNetwork: true
  volumes:
    - name: kubeconfig
      hostPath:
        path: /etc/kubernetes/admin.conf
`
