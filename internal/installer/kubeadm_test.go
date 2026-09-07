package installer

import (
	"strings"
	"testing"
)

func TestRenderInitConfig(t *testing.T) {
	spec := &InstallSpec{
		Role:                 RoleFirstMaster,
		K8sVersion:           "v1.35.7",
		PodCIDR:              "10.244.0.0/16",
		ServiceCIDR:          "10.96.0.0/12",
		ImageRepository:      "registry.example.com/k8s",
		ControlPlaneEndpoint: "10.0.0.100:6443",
		AdvertiseAddress:     "10.0.0.11",
		CertSANs:             []string{"api.example.com"},
		VIP:                  &VIPSpec{Enabled: true, Address: "10.0.0.100"},
	}
	out := renderInitConfig(spec)

	wants := []string{
		"apiVersion: kubeadm.k8s.io/v1beta4",
		"kind: InitConfiguration",
		"advertiseAddress: 10.0.0.11",
		"criSocket: unix:///run/containerd/containerd.sock",
		"kind: ClusterConfiguration",
		"kubernetesVersion: v1.35.7",
		"controlPlaneEndpoint: 10.0.0.100:6443",
		"imageRepository: registry.example.com/k8s",
		"podSubnet: 10.244.0.0/16",
		"serviceSubnet: 10.96.0.0/12",
		"certSANs:",
		"\"10.0.0.100\"",      // VIP
		"\"api.example.com\"", // 显式 SAN
		"\"10.0.0.11\"",       // 通告地址
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("init config 缺少片段 %q\n---\n%s", w, out)
		}
	}
}

func TestCollectCertSANsDedup(t *testing.T) {
	spec := &InstallSpec{
		ControlPlaneEndpoint: "10.0.0.100:6443",
		AdvertiseAddress:     "10.0.0.100", // 与 endpoint host 重复
		VIP:                  &VIPSpec{Address: "10.0.0.100"},
		CertSANs:             []string{"10.0.0.100", "extra.local"},
	}
	sans := collectCertSANs(spec)
	count := map[string]int{}
	for _, s := range sans {
		count[s]++
	}
	if count["10.0.0.100"] != 1 {
		t.Errorf("VIP/endpoint/advertise 重复未去重: %v", sans)
	}
	if count["extra.local"] != 1 {
		t.Errorf("缺少显式 SAN extra.local: %v", sans)
	}
}

func TestParseJoinCommand(t *testing.T) {
	cmd := "kubeadm join 10.0.0.100:6443 --token abcdef.0123456789abcdef " +
		"--discovery-token-ca-cert-hash sha256:deadbeef"
	token, hash, endpoint := parseJoinCommand(cmd)
	if endpoint != "10.0.0.100:6443" {
		t.Errorf("endpoint=%q", endpoint)
	}
	if token != "abcdef.0123456789abcdef" {
		t.Errorf("token=%q", token)
	}
	if hash != "sha256:deadbeef" {
		t.Errorf("hash=%q", hash)
	}
}

func TestApplyDefaults(t *testing.T) {
	s := &InstallSpec{Role: RoleWorker, K8sVersion: "1.35.7"}
	applyDefaults(s)
	if s.K8sVersion != "v1.35.7" {
		t.Errorf("版本未补 v 前缀: %q", s.K8sVersion)
	}
	if s.PodCIDR != DefaultPodCIDR || s.ServiceCIDR != DefaultServiceCIDR {
		t.Errorf("CIDR 默认值未填: pod=%q svc=%q", s.PodCIDR, s.ServiceCIDR)
	}
	if s.CNI == nil || s.CNI.Type != DefaultCNIType || s.CNI.Version != DefaultCiliumVer {
		t.Errorf("CNI 默认值未填: %+v", s.CNI)
	}
	if s.Artifact == nil || s.Artifact.Mode != ArtifactBundle {
		t.Errorf("Artifact 默认未填: %+v", s.Artifact)
	}
}

func TestKubeadmJoinArgsMaster(t *testing.T) {
	// 通过 renderInitConfig 之外的 join 分支间接校验参数拼装是稳定的：这里只验证 host 提取。
	if h := hostOf("10.0.0.100:6443"); h != "10.0.0.100" {
		t.Errorf("hostOf=%q", h)
	}
	if h := hostOf("lb.example.com"); h != "lb.example.com" {
		t.Errorf("hostOf 无端口应原样返回: %q", h)
	}
}
