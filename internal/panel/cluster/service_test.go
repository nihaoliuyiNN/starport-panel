package cluster

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"starport-panel/internal/agent"
	"starport-panel/internal/installer"
	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/store"
	"starport-panel/internal/panel/task"
	pb "starport-panel/internal/pb/agentv1"
)

// fakeHub 记录下发的 InstallSpec，按预设结果应答。
type fakeHub struct {
	mu       sync.Mutex
	online   map[uint64]bool
	specs    []*pb.InstallSpec
	install  func(spec *pb.InstallSpec) agenthub.Result
	execLine string // Exec 时回放的 marker 行
	execs    []execCall
	exec     func(nodeID uint64, script string) agenthub.Result // 为 nil 时一律成功
}

type execCall struct {
	nodeID uint64
	script string
}

func (f *fakeHub) Online(id uint64) bool { return f.online[id] }

func (f *fakeHub) Exec(_ context.Context, nodeID uint64, script string, _ time.Duration, onLog func(string)) (agenthub.Result, error) {
	f.mu.Lock()
	f.execs = append(f.execs, execCall{nodeID, script})
	f.mu.Unlock()
	onLog(f.execLine)
	if f.exec != nil {
		return f.exec(nodeID, script), nil
	}
	return agenthub.Result{OK: true}, nil
}

func (f *fakeHub) Install(_ context.Context, _ uint64, spec *pb.InstallSpec, onLog func(string)) (agenthub.Result, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	onLog("installing...")
	return f.install(spec), nil
}

func newEnv(t *testing.T) (*Service, *store.Store, *fakeHub) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hub := &fakeHub{online: map[uint64]bool{}}
	return New(st, hub, task.New(st)), st, hub
}

func waitTask(t *testing.T, st *store.Store, id uint64) store.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := st.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if tk.Done() {
			// onDone 回调在 FinishTask 之后才跑，稍等它落库
			time.Sleep(50 * time.Millisecond)
			return tk
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task timeout")
	return store.Task{}
}

func TestCreateValidation(t *testing.T) {
	svc, _, _ := newEnv(t)
	ctx := context.Background()
	var ce *Error
	cases := []struct {
		name string
		req  CreateRequest
		code string
	}{
		{"no name", CreateRequest{BundleURL: "http://x"}, "INVALID_ARGUMENT"},
		{"bad cidr", CreateRequest{Name: "a", PodCIDR: "nope", BundleURL: "http://x"}, "INVALID_ARGUMENT"},
		{"bundle without url", CreateRequest{Name: "a"}, "INVALID_ARGUMENT"},
		{"online cilium", CreateRequest{Name: "a", ArtifactMode: "online", CNI: "cilium"}, "INVALID_ARGUMENT"},
		{"bad addon", CreateRequest{Name: "a", BundleURL: "http://x", Addons: []string{"foo"}}, "INVALID_ARGUMENT"},
	}
	for _, tc := range cases {
		_, err := svc.Create(ctx, tc.req)
		var ce *Error
		if !asErr(err, &ce) || ce.Code != tc.code {
			t.Errorf("%s: want %s, got %v", tc.name, tc.code, err)
		}
	}
	c, err := svc.Create(ctx, CreateRequest{Name: "ok", ArtifactMode: "online", CNI: "calico", VIP: "10.0.0.100"})
	if err != nil {
		t.Fatal(err)
	}
	if c.K8sVersion != installer.DefaultK8sVersion || c.CNIVersion != installer.DefaultCalicoVer || c.ControlPlaneEndpoint != "10.0.0.100:6443" {
		t.Fatalf("defaults: %+v", c)
	}
	if _, err := svc.Create(ctx, CreateRequest{Name: "ok", BundleURL: "http://x"}); !asErr(err, &ce) || ce.Code != "CLUSTER_NAME_EXISTS" {
		t.Fatalf("dup name: %v", err)
	}
}

func asErr(err error, target **Error) bool { return errors.As(err, target) }

func TestFirstMasterThenWorker(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	var ce *Error

	c, err := svc.Create(ctx, CreateRequest{Name: "c1", BundleURL: "http://b/k8s.tgz", Addons: []string{"metrics-server"}})
	if err != nil {
		t.Fatal(err)
	}
	m1, _, _ := st.RegisterNode(agent.Facts{Hostname: "m1", InternalIP: "10.0.0.11"}, "v1")
	w1, _, _ := st.RegisterNode(agent.Facts{Hostname: "w1", InternalIP: "10.0.0.21"}, "v1")
	hub.online[m1] = true

	// 离线节点不能加
	if _, err := svc.AddNode(ctx, c.ID, w1, installer.RoleWorker); !asErr(err, &ce) || ce.Code != "NODE_OFFLINE" {
		t.Fatalf("offline: %v", err)
	}
	// 控制面没就绪不能加 worker
	hub.online[w1] = true
	if _, err := svc.AddNode(ctx, c.ID, w1, installer.RoleWorker); !asErr(err, &ce) || ce.Code != "CLUSTER_NOT_READY" {
		t.Fatalf("not ready: %v", err)
	}

	kubeconfig := "apiVersion: v1\nclusters: []\n"
	hub.install = func(spec *pb.InstallSpec) agenthub.Result {
		return agenthub.Result{OK: true, Install: &pb.InstallResult{
			Token: "tok.en", CaCertHash: "sha256:abc", CertificateKey: "ck",
			ControlPlaneEndpoint: spec.GetControlPlaneEndpoint(),
			KubeconfigB64:        base64.StdEncoding.EncodeToString([]byte(kubeconfig)),
		}}
	}
	taskID, err := svc.AddNode(ctx, c.ID, m1, installer.RoleFirstMaster)
	if err != nil {
		t.Fatal(err)
	}
	tk := waitTask(t, st, taskID)
	if tk.Status != store.TaskSucceeded {
		t.Fatalf("first master task: %+v", tk)
	}
	got, _ := st.GetCluster(ctx, c.ID)
	if got.Status != store.ClusterReady || got.Kubeconfig != kubeconfig || got.Join.Token != "tok.en" ||
		got.ControlPlaneEndpoint != "10.0.0.11:6443" {
		t.Fatalf("takeover: %+v", got)
	}
	spec := hub.specs[0]
	if spec.GetRole() != pb.NodeRole_NODE_ROLE_FIRST_MASTER || spec.GetAdvertiseAddress() != "10.0.0.11" ||
		spec.GetArtifact().GetMode() != pb.ArtifactMode_ARTIFACT_MODE_BUNDLE || spec.GetArtifact().GetBundleUrl() != "http://b/k8s.tgz" ||
		spec.GetCni().GetType() != pb.CniType_CNI_TYPE_CILIUM || len(spec.GetAddons()) != 1 || spec.GetJoin() != nil {
		t.Fatalf("first-master spec: %+v", spec)
	}
	// 同一台机不能再加
	if _, err := svc.AddNode(ctx, c.ID, m1, installer.RoleWorker); !asErr(err, &ce) || ce.Code != "NODE_ALREADY_MEMBER" {
		t.Fatalf("already member: %v", err)
	}
	// 已有控制面，不能再 first-master
	if _, err := svc.AddNode(ctx, c.ID, w1, installer.RoleFirstMaster); !asErr(err, &ce) || ce.Code != "CLUSTER_HAS_CONTROL_PLANE" {
		t.Fatalf("has control plane: %v", err)
	}

	// worker 加入：带 join 凭据（不带 certificateKey）
	hub.install = func(*pb.InstallSpec) agenthub.Result { return agenthub.Result{OK: true} }
	taskID, err = svc.AddNode(ctx, c.ID, w1, installer.RoleWorker)
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, st, taskID)
	spec = hub.specs[1]
	if spec.GetRole() != pb.NodeRole_NODE_ROLE_WORKER || spec.GetJoin().GetToken() != "tok.en" ||
		spec.GetJoin().GetCaCertHash() != "sha256:abc" || spec.GetJoin().GetCertificateKey() != "" ||
		spec.GetControlPlaneEndpoint() != "10.0.0.11:6443" {
		t.Fatalf("worker spec: %+v", spec)
	}
	ms, _ := st.ListMembers(ctx, c.ID)
	if len(ms) != 2 || ms[0].Status != store.MemberReady || ms[1].Status != store.MemberReady {
		t.Fatalf("members: %+v", ms)
	}
}

func TestJoinRefreshWhenStale(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	c, _ := svc.Create(ctx, CreateRequest{Name: "c", BundleURL: "http://b"})
	m1, _, _ := st.RegisterNode(agent.Facts{Hostname: "m1", InternalIP: "10.0.0.11"}, "v1")
	m2, _, _ := st.RegisterNode(agent.Facts{Hostname: "m2", InternalIP: "10.0.0.12"}, "v1")
	hub.online[m1], hub.online[m2] = true, true

	// 直接造一个 ready 集群，join 凭据 3 小时前签发（certificateKey 已过期）
	_ = st.SetClusterCredentials(ctx, c.ID, "kc", store.JoinCreds{Token: "old", CACertHash: "sha256:old", CertificateKey: "oldkey",
		IssuedAt: time.Now().Add(-3 * time.Hour)})
	_ = st.UpsertMember(ctx, store.Member{ClusterID: c.ID, NodeID: m1, Role: installer.RoleFirstMaster, Status: store.MemberReady})

	hub.execLine = "STARPORT_JOIN token=new.tok hash=sha256:new certkey=newkey"
	hub.install = func(*pb.InstallSpec) agenthub.Result { return agenthub.Result{OK: true} }
	taskID, err := svc.AddNode(ctx, c.ID, m2, installer.RoleJoinMaster)
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, st, taskID)
	spec := hub.specs[0]
	if spec.GetJoin().GetToken() != "new.tok" || spec.GetJoin().GetCertificateKey() != "newkey" {
		t.Fatalf("join-master should use refreshed creds: %+v", spec.GetJoin())
	}
	got, _ := st.GetCluster(ctx, c.ID)
	if got.Join.Token != "new.tok" || time.Since(got.Join.IssuedAt) > time.Minute {
		t.Fatalf("creds not persisted: %+v", got.Join)
	}
}

func TestFirstMasterFailure(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	c, _ := svc.Create(ctx, CreateRequest{Name: "c", BundleURL: "http://b"})
	m1, _, _ := st.RegisterNode(agent.Facts{Hostname: "m1", InternalIP: "10.0.0.11"}, "v1")
	hub.online[m1] = true
	hub.install = func(*pb.InstallSpec) agenthub.Result {
		return agenthub.Result{OK: false, ExitCode: 1, Err: &agent.Error{Code: installer.ErrPreflight, Message: "swap on"}}
	}
	taskID, err := svc.AddNode(ctx, c.ID, m1, installer.RoleFirstMaster)
	if err != nil {
		t.Fatal(err)
	}
	tk := waitTask(t, st, taskID)
	if tk.Status != store.TaskFailed || tk.ErrorCode != installer.ErrPreflight {
		t.Fatalf("task: %+v", tk)
	}
	got, _ := st.GetCluster(ctx, c.ID)
	m, _ := st.GetMember(ctx, c.ID, m1)
	if got.Status != store.ClusterFailed || m.Status != store.MemberFailed || m.Error == "" {
		t.Fatalf("cluster=%s member=%+v", got.Status, m)
	}
	// 失败后允许同机重试 first-master
	hub.install = func(*pb.InstallSpec) agenthub.Result {
		return agenthub.Result{OK: true, Install: &pb.InstallResult{Token: "t", CaCertHash: "h",
			KubeconfigB64: base64.StdEncoding.EncodeToString([]byte("kc"))}}
	}
	taskID, err = svc.AddNode(ctx, c.ID, m1, installer.RoleFirstMaster)
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, st, taskID)
	got, _ = st.GetCluster(ctx, c.ID)
	if got.Status != store.ClusterReady {
		t.Fatalf("retry: %s", got.Status)
	}
}
