// Package installer 是 starport-agent 内置的 Kubernetes 装机引擎：把面板下发的结构化
// InstallSpec 翻译成本机的一组装机阶段（os 准备 / containerd / kube 二进制 / kubeadm /
// kube-vip / CNI），逐阶段执行并流式回传日志，替代旧「控制面 SSH 进机器跑脚本」的方式。
//
// 该包自洽：只依赖标准库与系统命令（kubeadm/containerd/systemctl/apt...），不反向依赖
// agent，避免 import 环。agent 以类型别名复用这里的 InstallSpec/InstallResult 作为
// 隧道 JSON 契约。
package installer

import "fmt"

// 节点角色。
const (
	RoleFirstMaster = "first-master" // 首个控制面：kubeadm init --upload-certs
	RoleJoinMaster  = "join-master"  // 其余控制面：join --control-plane --certificate-key
	RoleWorker      = "worker"       // 工作节点：join
)

// CNI 类型。
const (
	CNICilium = "cilium"
	CNICalico = "calico"
)

// 制品来源：
//   - bundle : 节点无需外网/私有仓，全部制品（kube 二进制、containerd、CNI 插件、镜像 tar、
//     CNI/addon 清单、systemd 单元）由控制面托管的离线包提供。
//   - online : 节点需有外网（NAT）出口。二进制走 apt（默认阿里云镜像），控制面镜像走
//     registry.aliyuncs.com/google_containers，其余镜像经 containerd registry mirror（daocloud）
//     在线拉取；CNI/addon 清单用 agent 内嵌的（免节点侧拉 github）。CNI 仅支持 calico。
const (
	ArtifactBundle = "bundle"
	ArtifactOnline = "online"
)

// 在线模式国内默认镜像仓（kubeadm imageRepository）。
const cnImageRepository = "registry.aliyuncs.com/google_containers"

// 装机错误码（回传控制面按码分支/重试）。
const (
	ErrPreflight     = "INSTALL_PREFLIGHT_FAILED" // 预检未通过
	ErrStep          = "INSTALL_STEP_FAILED"      // 某装机阶段失败
	ErrUnsupportedOS = "INSTALL_UNSUPPORTED_OS"   // 不支持的操作系统
	ErrCancelled     = "CANCELLED"                // 被控制面取消
)

// 复用退役 server/internal/k8sinstall 的成熟默认值。
const (
	DefaultK8sVersion  = "v1.35.7"
	DefaultPodCIDR     = "10.244.0.0/16"
	DefaultServiceCIDR = "10.96.0.0/12"
	DefaultPauseTag    = "3.10.1"
	DefaultCNIType     = CNICilium
	DefaultCiliumVer   = "1.16.5"
	DefaultCalicoVer   = "v3.29.1"

	kubeManifestDir = "/etc/kubernetes/manifests"
	adminConf       = "/etc/kubernetes/admin.conf"
	kubeletConf     = "/etc/kubernetes/kubelet.conf"
)

// LogFunc 阶段日志回调（逐行；由 conn.go 转成 log 帧回传控制面）。
type LogFunc func(string)

// Fail 结构化装机失败：Code 供控制面分支，Retryable 指示是否可重试。
type Fail struct {
	Code      string
	Message   string
	Retryable bool
}

func (f *Fail) Error() string { return f.Code + ": " + f.Message }

func fail(code, msg string) *Fail { return &Fail{Code: code, Message: msg} }

func failf(code, format string, a ...any) *Fail {
	return &Fail{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Options agent 侧运行时参数。
type Options struct {
	DataDir string // 临时文件 / bundle 缓存目录
}

// InstallSpec 面板下发的结构化装机指令（替代脚本）。agent 按 Role 编排本机装机阶段。
type InstallSpec struct {
	Role                 string        `json:"role"`                       // first-master | join-master | worker
	K8sVersion           string        `json:"k8sVersion"`                 // 如 v1.35.7
	PodCIDR              string        `json:"podCIDR"`                    // 如 10.244.0.0/16
	ServiceCIDR          string        `json:"serviceCIDR"`                // 如 10.96.0.0/12
	ImageRepository      string        `json:"imageRepository,omitempty"`  // 镜像仓前缀，空则用官方
	ControlPlaneEndpoint string        `json:"controlPlaneEndpoint"`       // VIP:6443 或外部 LB
	AdvertiseAddress     string        `json:"advertiseAddress,omitempty"` // 本节点 apiserver 通告 IP
	CertSANs             []string      `json:"certSANs,omitempty"`
	VIP                  *VIPSpec      `json:"vip,omitempty"`      // 内置 kube-vip
	CNI                  *CNISpec      `json:"cni,omitempty"`      // 首 master 装 CNI
	Join                 *JoinSpec     `json:"join,omitempty"`     // join-master / worker 用
	Artifact             *ArtifactSpec `json:"artifact,omitempty"` // 制品来源（离线/在线）
	Addons               []string      `json:"addons,omitempty"`   // 首 master 装完 CNI 后按序 apply 的 add-on（清单来自离线包 addons/）
}

// 内置支持的 add-on 名称（对应离线包 addons/<name>.yaml，纯 kubectl apply 型）。
const (
	AddonIngressNginx  = "ingress-nginx"
	AddonMetricsServer = "metrics-server"
	AddonCertManager   = "cert-manager"
)

// VIPSpec kube-vip 静态 Pod 参数（L2/ARP）。
type VIPSpec struct {
	Enabled   bool   `json:"enabled"`
	Address   string `json:"address"`             // VIP，如 10.0.0.100
	Interface string `json:"interface,omitempty"` // 绑定网卡，空则自动探测
	Version   string `json:"version,omitempty"`   // kube-vip 镜像 tag，空用内置默认
}

// CNISpec 网络插件。
type CNISpec struct {
	Type    string `json:"type"`    // cilium | calico
	Version string `json:"version"` // 如 1.16.5 / v3.29.1
}

// JoinSpec 加入集群所需凭据（由首 master 装机后回传）。
type JoinSpec struct {
	Token          string `json:"token,omitempty"`
	CACertHash     string `json:"caCertHash,omitempty"`     // sha256:...
	CertificateKey string `json:"certificateKey,omitempty"` // 仅 join-master 用（2h 有效）
}

// ArtifactSpec 制品来源。
type ArtifactSpec struct {
	Mode        string `json:"mode"`                  // bundle | online
	BundleURL   string `json:"bundleUrl,omitempty"`   // bundle 模式：离线包地址（tar.gz 或 .parts.json 分卷清单）
	UseCNMirror bool   `json:"useCnMirror,omitempty"` // online 模式：用国内镜像源（apt 阿里云 + registry mirror）
}

// InstallResult 装机终态的结构化输出（随 result 帧的 Data 回传）。
// 首 master 回传 join 凭据与 kubeconfig，供控制面编排其余节点加入。
type InstallResult struct {
	JoinCommand          string `json:"joinCommand,omitempty"`
	Token                string `json:"token,omitempty"`
	CACertHash           string `json:"caCertHash,omitempty"`
	CertificateKey       string `json:"certificateKey,omitempty"`
	ControlPlaneEndpoint string `json:"controlPlaneEndpoint,omitempty"`
	KubeconfigB64        string `json:"kubeconfigB64,omitempty"`
}
