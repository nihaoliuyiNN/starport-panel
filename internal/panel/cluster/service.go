// Package cluster 是集群生命周期编排：建集群记录 → 首 master 装机（kubeadm init）→ 接管
// kubeconfig 与 join 凭据 → 其余 master / worker 加入。所有对节点的动作经 agenthub 下发，
// 以异步任务（internal/panel/task）执行并落日志。
package cluster

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"starport-panel/internal/installer"
	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/store"
	"starport-panel/internal/panel/task"
	pb "starport-panel/internal/pb/agentv1"
)

// Hub 编排层需要的节点通道能力（由 agenthub.Hub 实现；测试可替换）。
type Hub interface {
	Online(nodeID uint64) bool
	Exec(ctx context.Context, nodeID uint64, script string, timeout time.Duration, onLog func(string)) (agenthub.Result, error)
	Install(ctx context.Context, nodeID uint64, spec *pb.InstallSpec, onLog func(string)) (agenthub.Result, error)
}

// Error 业务错误：Code 供调用方分支，Status 为建议的 HTTP 状态码。
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func badRequest(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), Status: 400}
}

func conflict(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), Status: 409}
}

// join 凭据有效期：kubeadm token 默认 24h、certificate-key 2h；提前刷新留余量。
const (
	tokenMaxAge   = 20 * time.Hour
	certKeyMaxAge = 90 * time.Minute
)

// Service 见包注释。
type Service struct {
	store *store.Store
	hub   Hub
	tasks *task.Runner
}

// New 建服务。
func New(st *store.Store, hub Hub, tasks *task.Runner) *Service {
	return &Service{store: st, hub: hub, tasks: tasks}
}

// CreateRequest 建集群参数；空字段取默认值。
type CreateRequest struct {
	Name                 string   `json:"name"`
	K8sVersion           string   `json:"k8sVersion"`
	PodCIDR              string   `json:"podCIDR"`
	ServiceCIDR          string   `json:"serviceCIDR"`
	ControlPlaneEndpoint string   `json:"controlPlaneEndpoint"` // host:port；空则 VIP:6443 或首 master IP:6443
	VIP                  string   `json:"vip"`
	VIPInterface         string   `json:"vipInterface"`
	CNI                  string   `json:"cni"`
	CNIVersion           string   `json:"cniVersion"`
	Addons               []string `json:"addons"`
	ArtifactMode         string   `json:"artifactMode"` // bundle | online
	BundleURL            string   `json:"bundleUrl"`
	UseCNMirror          bool     `json:"useCnMirror"`
}

// Create 校验并建集群记录（尚无控制面，status=created）。
func (s *Service) Create(ctx context.Context, req CreateRequest) (store.Cluster, error) {
	c, err := normalize(req)
	if err != nil {
		return store.Cluster{}, err
	}
	if err := s.store.CreateCluster(ctx, &c); err != nil {
		if store.IsConflict(err) {
			return store.Cluster{}, conflict("CLUSTER_NAME_EXISTS", "集群名 %q 已存在", c.Name)
		}
		return store.Cluster{}, err
	}
	return c, nil
}

func normalize(req CreateRequest) (store.Cluster, error) {
	c := store.Cluster{
		Name:                 strings.TrimSpace(req.Name),
		K8sVersion:           firstNonEmpty(strings.TrimSpace(req.K8sVersion), installer.DefaultK8sVersion),
		PodCIDR:              firstNonEmpty(strings.TrimSpace(req.PodCIDR), installer.DefaultPodCIDR),
		ServiceCIDR:          firstNonEmpty(strings.TrimSpace(req.ServiceCIDR), installer.DefaultServiceCIDR),
		ControlPlaneEndpoint: strings.TrimSpace(req.ControlPlaneEndpoint),
		VIP:                  strings.TrimSpace(req.VIP),
		VIPInterface:         strings.TrimSpace(req.VIPInterface),
		CNI:                  firstNonEmpty(strings.TrimSpace(req.CNI), installer.DefaultCNIType),
		CNIVersion:           strings.TrimSpace(req.CNIVersion),
		Addons:               req.Addons,
		ArtifactMode:         firstNonEmpty(strings.TrimSpace(req.ArtifactMode), installer.ArtifactBundle),
		BundleURL:            strings.TrimSpace(req.BundleURL),
		UseCNMirror:          req.UseCNMirror,
	}
	if c.Name == "" {
		return c, badRequest("INVALID_ARGUMENT", "name 必填")
	}
	if !strings.HasPrefix(c.K8sVersion, "v") {
		return c, badRequest("INVALID_ARGUMENT", "k8sVersion 须形如 v1.35.7")
	}
	for _, cidr := range []string{c.PodCIDR, c.ServiceCIDR} {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return c, badRequest("INVALID_ARGUMENT", "非法 CIDR %q", cidr)
		}
	}
	switch c.CNI {
	case installer.CNICilium:
		c.CNIVersion = firstNonEmpty(c.CNIVersion, installer.DefaultCiliumVer)
	case installer.CNICalico:
		c.CNIVersion = firstNonEmpty(c.CNIVersion, installer.DefaultCalicoVer)
	default:
		return c, badRequest("INVALID_ARGUMENT", "cni 只支持 cilium / calico")
	}
	switch c.ArtifactMode {
	case installer.ArtifactBundle:
		u, err := url.Parse(c.BundleURL)
		if c.BundleURL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return c, badRequest("INVALID_ARGUMENT", "bundle 模式须提供 http(s) 的 bundleUrl")
		}
	case installer.ArtifactOnline:
		if c.CNI != installer.CNICalico {
			return c, badRequest("INVALID_ARGUMENT", "online 模式仅支持 calico（cilium 需 helm）")
		}
	default:
		return c, badRequest("INVALID_ARGUMENT", "artifactMode 只支持 bundle / online")
	}
	if c.VIP != "" && net.ParseIP(c.VIP) == nil {
		return c, badRequest("INVALID_ARGUMENT", "vip 不是合法 IP")
	}
	if c.ControlPlaneEndpoint == "" && c.VIP != "" {
		c.ControlPlaneEndpoint = net.JoinHostPort(c.VIP, "6443")
	}
	if c.ControlPlaneEndpoint != "" {
		if _, _, err := net.SplitHostPort(c.ControlPlaneEndpoint); err != nil {
			return c, badRequest("INVALID_ARGUMENT", "controlPlaneEndpoint 须为 host:port")
		}
	}
	if c.Addons == nil {
		c.Addons = []string{}
	}
	for _, a := range c.Addons {
		switch a {
		case installer.AddonIngressNginx, installer.AddonMetricsServer, installer.AddonCertManager:
		default:
			return c, badRequest("INVALID_ARGUMENT", "未知 addon %q", a)
		}
	}
	return c, nil
}

// Get 集群详情。
func (s *Service) Get(ctx context.Context, id uint64) (store.Cluster, error) {
	return s.store.GetCluster(ctx, id)
}

// List 全部集群。
func (s *Service) List(ctx context.Context) ([]store.Cluster, error) {
	return s.store.ListClusters(ctx)
}

// Members 集群成员。
func (s *Service) Members(ctx context.Context, id uint64) ([]store.Member, error) {
	return s.store.ListMembers(ctx, id)
}

// Kubeconfig 集群 admin kubeconfig；控制面未就绪时报错。
func (s *Service) Kubeconfig(ctx context.Context, id uint64) (string, error) {
	c, err := s.store.GetCluster(ctx, id)
	if err != nil {
		return "", err
	}
	if c.Kubeconfig == "" {
		return "", conflict("CLUSTER_NOT_READY", "集群控制面尚未就绪")
	}
	return c.Kubeconfig, nil
}

// AddNode 把节点以指定角色装进集群，返回装机任务 ID。
//   - first-master：集群须处于 created / failed；装机成功后接管 kubeconfig 与 join 凭据，集群置 ready。
//   - join-master / worker：集群须 ready；join 凭据过期则先在现有 master 上刷新。
func (s *Service) AddNode(ctx context.Context, clusterID, nodeID uint64, role string) (uint64, error) {
	c, err := s.store.GetCluster(ctx, clusterID)
	if err != nil {
		return 0, err
	}
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	if !s.hub.Online(nodeID) {
		return 0, conflict("NODE_OFFLINE", "节点 %d 不在线", nodeID)
	}
	if node.Facts.InternalIP == "" {
		return 0, badRequest("NODE_NO_IP", "节点 %d 未上报内网 IP", nodeID)
	}
	if err := s.checkMembership(ctx, clusterID, nodeID); err != nil {
		return 0, err
	}

	switch role {
	case installer.RoleFirstMaster:
		if c.Status != store.ClusterCreated && c.Status != store.ClusterFailed {
			return 0, conflict("CLUSTER_HAS_CONTROL_PLANE", "集群已有控制面（状态 %s），请以 join-master / worker 加入", c.Status)
		}
		if c.ControlPlaneEndpoint == "" {
			c.ControlPlaneEndpoint = net.JoinHostPort(node.Facts.InternalIP, "6443")
			if err := s.store.SetClusterEndpoint(ctx, c.ID, c.ControlPlaneEndpoint); err != nil {
				return 0, err
			}
		}
		if err := s.store.SetClusterStatus(ctx, c.ID, store.ClusterInstalling); err != nil {
			return 0, err
		}
	case installer.RoleJoinMaster, installer.RoleWorker:
		if c.Status != store.ClusterReady {
			return 0, conflict("CLUSTER_NOT_READY", "集群控制面未就绪（状态 %s）", c.Status)
		}
		if err := s.ensureFreshJoin(ctx, &c, role == installer.RoleJoinMaster); err != nil {
			return 0, err
		}
	default:
		return 0, badRequest("INVALID_ARGUMENT", "role 只支持 first-master / join-master / worker")
	}

	spec := buildSpec(c, node, role)
	member := store.Member{ClusterID: c.ID, NodeID: nodeID, Role: role, Status: store.MemberInstalling}
	if err := s.store.UpsertMember(ctx, member); err != nil {
		return 0, err
	}
	taskID, err := s.tasks.Start(store.TaskInstall, nodeID, c.ID,
		func(ctx context.Context, logf func(string)) (agenthub.Result, error) {
			logf(fmt.Sprintf("开始装机 role=%s k8s=%s endpoint=%s artifact=%s", role, c.K8sVersion, c.ControlPlaneEndpoint, c.ArtifactMode))
			return s.hub.Install(ctx, nodeID, spec, logf)
		},
		s.onInstallDone(role))
	if err != nil {
		_ = s.store.SetMemberStatus(ctx, c.ID, nodeID, store.MemberFailed, err.Error())
		return 0, err
	}
	_ = s.store.SetMemberTask(ctx, c.ID, nodeID, taskID)
	return taskID, nil
}

// checkMembership 一台机只属于一个集群；同集群上次失败的允许重试。
func (s *Service) checkMembership(ctx context.Context, clusterID, nodeID uint64) error {
	ms, err := s.store.NodeMemberships(ctx, nodeID)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.ClusterID != clusterID {
			return conflict("NODE_ALREADY_MEMBER", "节点 %d 已属于集群 %d", nodeID, m.ClusterID)
		}
		if m.Status != store.MemberFailed {
			return conflict("NODE_ALREADY_MEMBER", "节点 %d 已在本集群（状态 %s）", nodeID, m.Status)
		}
	}
	return nil
}

// onInstallDone 装机任务终态回调：推进成员/集群状态，首 master 成功时接管凭据。
func (s *Service) onInstallDone(role string) task.OnDone {
	return func(ctx context.Context, t store.Task, res agenthub.Result) {
		if !res.OK {
			msg := t.ErrorMessage
			if msg == "" {
				msg = "装机失败"
			}
			_ = s.store.SetMemberStatus(ctx, t.ClusterID, t.NodeID, store.MemberFailed, msg)
			if role == installer.RoleFirstMaster {
				_ = s.store.SetClusterStatus(ctx, t.ClusterID, store.ClusterFailed)
			}
			return
		}
		if role == installer.RoleFirstMaster {
			if err := s.takeOver(ctx, t.ClusterID, res.Install); err != nil {
				_ = s.store.SetMemberStatus(ctx, t.ClusterID, t.NodeID, store.MemberFailed, err.Error())
				_ = s.store.SetClusterStatus(ctx, t.ClusterID, store.ClusterFailed)
				return
			}
		}
		_ = s.store.SetMemberStatus(ctx, t.ClusterID, t.NodeID, store.MemberReady, "")
	}
}

// takeOver 首 master 回传的 InstallResult → kubeconfig + join 凭据入库，集群置 ready。
func (s *Service) takeOver(ctx context.Context, clusterID uint64, r *pb.InstallResult) error {
	if r == nil || r.GetKubeconfigB64() == "" {
		return errors.New("首 master 未回传 kubeconfig")
	}
	kc, err := base64.StdEncoding.DecodeString(r.GetKubeconfigB64())
	if err != nil {
		return fmt.Errorf("kubeconfig base64 解码失败: %w", err)
	}
	if r.GetToken() == "" || r.GetCaCertHash() == "" {
		return errors.New("首 master 未回传 join token / ca hash")
	}
	join := store.JoinCreds{
		Token:          r.GetToken(),
		CACertHash:     r.GetCaCertHash(),
		CertificateKey: r.GetCertificateKey(),
		IssuedAt:       time.Now(),
	}
	if ep := r.GetControlPlaneEndpoint(); ep != "" {
		_ = s.store.SetClusterEndpoint(ctx, clusterID, ep)
	}
	return s.store.SetClusterCredentials(ctx, clusterID, string(kc), join)
}

// ensureFreshJoin join 凭据过期则在一台在线的 ready master 上重新签发。
func (s *Service) ensureFreshJoin(ctx context.Context, c *store.Cluster, needCertKey bool) error {
	age := time.Since(c.Join.IssuedAt)
	fresh := c.Join.Token != "" && age < tokenMaxAge && (!needCertKey || (c.Join.CertificateKey != "" && age < certKeyMaxAge))
	if fresh {
		return nil
	}
	master, err := s.readyMaster(ctx, c.ID)
	if err != nil {
		return err
	}
	join, err := s.issueJoin(ctx, master)
	if err != nil {
		return err
	}
	if err := s.store.SetJoinCreds(ctx, c.ID, join); err != nil {
		return err
	}
	c.Join = join
	return nil
}

// readyMaster 找一台在线且 ready 的控制面节点。
func (s *Service) readyMaster(ctx context.Context, clusterID uint64) (uint64, error) {
	ms, err := s.store.ListMembers(ctx, clusterID)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if m.Status == store.MemberReady && (m.Role == installer.RoleFirstMaster || m.Role == installer.RoleJoinMaster) && s.hub.Online(m.NodeID) {
			return m.NodeID, nil
		}
	}
	return 0, conflict("NO_ONLINE_MASTER", "没有在线的控制面节点可签发 join 凭据")
}

// joinScript 在 master 上重新签发 token / 上传 certs，单行 marker 输出便于解析。
const joinScript = `set -euo pipefail
TOKEN=$(kubeadm token create --ttl 24h)
HASH=$(openssl x509 -pubkey -in /etc/kubernetes/pki/ca.crt | openssl rsa -pubin -outform der 2>/dev/null | openssl dgst -sha256 -hex | sed 's/^.* //')
CERTKEY=$(kubeadm init phase upload-certs --upload-certs 2>/dev/null | tail -n1)
echo "STARPORT_JOIN token=$TOKEN hash=sha256:$HASH certkey=$CERTKEY"
`

func (s *Service) issueJoin(ctx context.Context, masterNodeID uint64) (store.JoinCreds, error) {
	var marker string
	res, err := s.hub.Exec(ctx, masterNodeID, joinScript, 2*time.Minute, func(line string) {
		if strings.HasPrefix(line, "STARPORT_JOIN ") {
			marker = line
		}
	})
	if err != nil {
		return store.JoinCreds{}, fmt.Errorf("刷新 join 凭据: %w", err)
	}
	if !res.OK || marker == "" {
		msg := "脚本执行失败"
		if res.Err != nil {
			msg = res.Err.Message
		}
		return store.JoinCreds{}, conflict("JOIN_REFRESH_FAILED", "在 master 上刷新 join 凭据失败: %s", msg)
	}
	join := store.JoinCreds{IssuedAt: time.Now()}
	for _, kv := range strings.Fields(strings.TrimPrefix(marker, "STARPORT_JOIN ")) {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "token":
			join.Token = v
		case "hash":
			join.CACertHash = v
		case "certkey":
			join.CertificateKey = v
		}
	}
	if join.Token == "" || join.CACertHash == "" {
		return store.JoinCreds{}, conflict("JOIN_REFRESH_FAILED", "刷新输出缺少 token / hash")
	}
	return join, nil
}

// buildSpec 集群 + 节点 + 角色 → 下发给 agent 的结构化装机指令。
func buildSpec(c store.Cluster, node store.Node, role string) *pb.InstallSpec {
	spec := &pb.InstallSpec{
		Role:                 roleEnum(role),
		K8SVersion:           c.K8sVersion,
		PodCidr:              c.PodCIDR,
		ServiceCidr:          c.ServiceCIDR,
		ControlPlaneEndpoint: c.ControlPlaneEndpoint,
		AdvertiseAddress:     node.Facts.InternalIP,
		Cni:                  &pb.CniSpec{Type: cniEnum(c.CNI), Version: c.CNIVersion},
		Artifact: &pb.ArtifactSpec{
			Mode:        artifactEnum(c.ArtifactMode),
			BundleUrl:   c.BundleURL,
			UseCnMirror: c.UseCNMirror,
		},
		Addons: c.Addons,
	}
	// 证书 SAN：VIP、入口主机、本机 IP，去重
	sans := map[string]bool{}
	for _, h := range []string{c.VIP, hostOf(c.ControlPlaneEndpoint), node.Facts.InternalIP} {
		if h != "" && !sans[h] {
			sans[h] = true
			spec.CertSans = append(spec.CertSans, h)
		}
	}
	if c.VIP != "" {
		spec.Vip = &pb.VipSpec{Enabled: true, Address: c.VIP, Interface: c.VIPInterface}
	}
	if role != installer.RoleFirstMaster {
		spec.Join = &pb.JoinSpec{Token: c.Join.Token, CaCertHash: c.Join.CACertHash}
		if role == installer.RoleJoinMaster {
			spec.Join.CertificateKey = c.Join.CertificateKey
		}
	}
	return spec
}

func roleEnum(role string) pb.NodeRole {
	switch role {
	case installer.RoleFirstMaster:
		return pb.NodeRole_NODE_ROLE_FIRST_MASTER
	case installer.RoleJoinMaster:
		return pb.NodeRole_NODE_ROLE_JOIN_MASTER
	case installer.RoleWorker:
		return pb.NodeRole_NODE_ROLE_WORKER
	}
	return pb.NodeRole_NODE_ROLE_UNSPECIFIED
}

func cniEnum(cni string) pb.CniType {
	switch cni {
	case installer.CNICilium:
		return pb.CniType_CNI_TYPE_CILIUM
	case installer.CNICalico:
		return pb.CniType_CNI_TYPE_CALICO
	}
	return pb.CniType_CNI_TYPE_UNSPECIFIED
}

func artifactEnum(mode string) pb.ArtifactMode {
	switch mode {
	case installer.ArtifactBundle:
		return pb.ArtifactMode_ARTIFACT_MODE_BUNDLE
	case installer.ArtifactOnline:
		return pb.ArtifactMode_ARTIFACT_MODE_ONLINE
	}
	return pb.ArtifactMode_ARTIFACT_MODE_UNSPECIFIED
}

func hostOf(hostport string) string {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return h
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
