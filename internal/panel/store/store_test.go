package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"starport-panel/internal/agent"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNodes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	facts := agent.Facts{Hostname: "n1", InternalIP: "10.0.0.1", OS: "linux", Arch: "amd64", CPUCores: 4}

	id, tok, err := s.RegisterNode(facts, "v1")
	if err != nil || id == 0 || tok == "" {
		t.Fatalf("register: id=%d tok=%q err=%v", id, tok, err)
	}
	if got, ok := s.Authenticate(tok); !ok || got != id {
		t.Fatalf("authenticate: got=%d ok=%v", got, ok)
	}
	// 同机重装：复用 ID、旧令牌失效
	id2, tok2, _ := s.RegisterNode(facts, "v2")
	if id2 != id || tok2 == tok {
		t.Fatalf("re-register should reuse id and rotate token: id2=%d", id2)
	}
	if _, ok := s.Authenticate(tok); ok {
		t.Fatal("old token must be revoked")
	}

	s.NodeOnline(id, "v2", facts)
	n, err := s.GetNode(ctx, id)
	if err != nil || !n.Online || n.AgentVersion != "v2" || n.Facts.CPUCores != 4 {
		t.Fatalf("online: %+v err=%v", n, err)
	}
	if err := s.MarkAllOffline(); err != nil {
		t.Fatal(err)
	}
	n, _ = s.GetNode(ctx, id)
	if n.Online {
		t.Fatal("MarkAllOffline")
	}
	if _, err := s.GetNode(ctx, 999); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRegisterDedupByMachineID(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	// 老 agent（无 machine-id）注册，之后升级到带 machine-id 的版本：按 hostname+ip 认出并补上 machine_id
	a := agent.Facts{Hostname: "n1", InternalIP: "10.0.0.1"}
	id, _, _ := s.RegisterNode(a, "v1")
	a.MachineID = "mid-aaaa"
	id2, _, _ := s.RegisterNode(a, "v2")
	if id2 != id {
		t.Fatalf("upgrade path: want %d got %d", id, id2)
	}
	n, _ := s.GetNode(ctx, id)
	if n.Facts.MachineID != "mid-aaaa" {
		t.Fatalf("machine_id not backfilled: %+v", n.Facts)
	}

	// 换 IP + 改主机名，machine-id 不变：仍是同一节点，且回写新 hostname/ip
	b := agent.Facts{Hostname: "renamed", InternalIP: "10.0.0.9", MachineID: "mid-aaaa"}
	id3, _, _ := s.RegisterNode(b, "v2")
	if id3 != id {
		t.Fatalf("same machine-id must reuse node: %d vs %d", id3, id)
	}
	n, _ = s.GetNode(ctx, id)
	if n.Facts.Hostname != "renamed" || n.Facts.InternalIP != "10.0.0.9" {
		t.Fatalf("facts not refreshed: %+v", n.Facts)
	}

	// 另一台机器碰巧同 hostname+ip（克隆镜像换机）但 machine-id 不同：新节点
	c := agent.Facts{Hostname: "renamed", InternalIP: "10.0.0.9", MachineID: "mid-bbbb"}
	id4, _, _ := s.RegisterNode(c, "v2")
	if id4 == id {
		t.Fatal("different machine-id must not collide")
	}

	// 删除：不存在 → ErrNotFound；删后令牌失效
	_, tok, _ := s.RegisterNode(agent.Facts{Hostname: "x", InternalIP: "1.1.1.1", MachineID: "mid-x"}, "v")
	nodes, _ := s.ListNodes(ctx)
	if err := s.DeleteNode(ctx, nodes[len(nodes)-1].ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate(tok); ok {
		t.Fatal("token of deleted node must fail")
	}
	if err := s.DeleteNode(ctx, 9999); err != ErrNotFound {
		t.Fatalf("want ErrNotFound got %v", err)
	}
}

func TestClustersAndTasks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	c := Cluster{Name: "prod", K8sVersion: "v1.35.7", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		CNI: "cilium", CNIVersion: "1.16.5", Addons: []string{"metrics-server"}, ArtifactMode: "bundle", BundleURL: "http://x/b.tgz"}
	if err := s.CreateCluster(ctx, &c); err != nil {
		t.Fatal(err)
	}
	dup := Cluster{Name: "prod", K8sVersion: "v1", PodCIDR: "a", ServiceCIDR: "b", CNI: "c", ArtifactMode: "d"}
	if err := s.CreateCluster(ctx, &dup); !IsConflict(err) {
		t.Fatalf("want conflict, got %v", err)
	}

	join := JoinCreds{Token: "abc.def", CACertHash: "sha256:1", CertificateKey: "k", IssuedAt: time.Now()}
	if err := s.SetClusterCredentials(ctx, c.ID, "apiVersion: v1", join); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCluster(ctx, c.ID)
	if err != nil || got.Status != ClusterReady || got.Kubeconfig != "apiVersion: v1" || got.Join.Token != "abc.def" ||
		len(got.Addons) != 1 || got.Join.IssuedAt.IsZero() {
		t.Fatalf("credentials: %+v err=%v", got, err)
	}

	nodeID, _, _ := s.RegisterNode(agent.Facts{Hostname: "m1", InternalIP: "10.0.0.2"}, "v1")
	if err := s.UpsertMember(ctx, Member{ClusterID: c.ID, NodeID: nodeID, Role: "first-master", Status: MemberInstalling}); err != nil {
		t.Fatal(err)
	}
	taskID, err := s.CreateTask(ctx, TaskInstall, nodeID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetMemberTask(ctx, c.ID, nodeID, taskID)
	_ = s.SetMemberStatus(ctx, c.ID, nodeID, MemberReady, "")
	ms, _ := s.ListMembers(ctx, c.ID)
	if len(ms) != 1 || ms[0].TaskID != taskID || ms[0].Status != MemberReady {
		t.Fatalf("members: %+v", ms)
	}

	for _, l := range []string{"a", "b", "c"} {
		if err := s.AppendTaskLog(ctx, taskID, l); err != nil {
			t.Fatal(err)
		}
	}
	logs, _ := s.TaskLogs(ctx, taskID, 1, 0)
	if len(logs) != 2 || logs[0].Seq != 2 || logs[1].Line != "c" {
		t.Fatalf("logs after=1: %+v", logs)
	}
	if err := s.FinishTask(ctx, taskID, TaskSucceeded, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	tk, _ := s.GetTask(ctx, taskID)
	if !tk.Done() || tk.FinishedAt == nil {
		t.Fatalf("task: %+v", tk)
	}

	// 面板重启：running 任务判失败
	t2, _ := s.CreateTask(ctx, TaskExec, nodeID, 0)
	n, err := s.FailRunningTasks(ctx)
	if err != nil || n != 1 {
		t.Fatalf("FailRunningTasks: n=%d err=%v", n, err)
	}
	tk2, _ := s.GetTask(ctx, t2)
	if tk2.Status != TaskFailed || tk2.ErrorCode != "PANEL_RESTARTED" {
		t.Fatalf("task2: %+v", tk2)
	}
}
