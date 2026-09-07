package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"starport-panel/internal/agent"
	"starport-panel/internal/installer"
	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/store"
)

// readyCluster 直接造一个 ready 集群：m1 首 master、w1 worker，都在线。
func readyCluster(t *testing.T, svc *Service, st *store.Store, hub *fakeHub) (store.Cluster, uint64, uint64) {
	t.Helper()
	ctx := context.Background()
	c, err := svc.Create(ctx, CreateRequest{Name: "c", BundleURL: "http://b"})
	if err != nil {
		t.Fatal(err)
	}
	m1, _, _ := st.RegisterNode(agent.Facts{Hostname: "M1.local", InternalIP: "10.0.0.11"}, "v1")
	w1, _, _ := st.RegisterNode(agent.Facts{Hostname: "w1", InternalIP: "10.0.0.21"}, "v1")
	hub.online[m1], hub.online[w1] = true, true
	_ = st.SetClusterCredentials(ctx, c.ID, "kc", store.JoinCreds{Token: "t", CACertHash: "h", IssuedAt: time.Now()})
	_ = st.UpsertMember(ctx, store.Member{ClusterID: c.ID, NodeID: m1, Role: installer.RoleFirstMaster, Status: store.MemberReady})
	_ = st.UpsertMember(ctx, store.Member{ClusterID: c.ID, NodeID: w1, Role: installer.RoleWorker, Status: store.MemberReady})
	c, _ = st.GetCluster(ctx, c.ID)
	return c, m1, w1
}

func TestRemoveWorker(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	c, m1, w1 := readyCluster(t, svc, st, hub)

	taskID, err := svc.RemoveNode(ctx, c.ID, w1)
	if err != nil {
		t.Fatal(err)
	}
	// 发起后成员立即进入 removing
	if m, _ := st.GetMember(ctx, c.ID, w1); m.Status != store.MemberRemoving || m.TaskID != taskID {
		t.Fatalf("member after start: %+v", m)
	}
	tk := waitTask(t, st, taskID)
	if tk.Status != store.TaskSucceeded || tk.Kind != store.TaskRemove {
		t.Fatalf("task: %+v", tk)
	}
	// 顺序：先在 m1 上 drain w1（hostname 小写），再在 w1 上 reset
	if len(hub.execs) != 2 {
		t.Fatalf("execs: %+v", hub.execs)
	}
	if hub.execs[0].nodeID != m1 || !strings.Contains(hub.execs[0].script, `NODE="w1"`) ||
		!strings.Contains(hub.execs[0].script, "kubectl drain") || !strings.Contains(hub.execs[0].script, "kubectl delete node") {
		t.Fatalf("drain call: %+v", hub.execs[0])
	}
	if hub.execs[1].nodeID != w1 || !strings.Contains(hub.execs[1].script, "kubeadm reset") {
		t.Fatalf("reset call: %+v", hub.execs[1])
	}
	if _, err := st.GetMember(ctx, c.ID, w1); err == nil {
		t.Fatal("member should be deleted")
	}
	ms, _ := st.ListMembers(ctx, c.ID)
	if len(ms) != 1 || ms[0].NodeID != m1 {
		t.Fatalf("remaining members: %+v", ms)
	}
}

func TestRemoveWorkerOffline(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	c, m1, w1 := readyCluster(t, svc, st, hub)
	hub.online[w1] = false

	taskID, err := svc.RemoveNode(ctx, c.ID, w1)
	if err != nil {
		t.Fatal(err)
	}
	tk := waitTask(t, st, taskID)
	if tk.Status != store.TaskSucceeded {
		t.Fatalf("task: %+v", tk)
	}
	// 只在 master 上摘除，不对离线节点下发 reset
	if len(hub.execs) != 1 || hub.execs[0].nodeID != m1 {
		t.Fatalf("execs: %+v", hub.execs)
	}
	if _, err := st.GetMember(ctx, c.ID, w1); err == nil {
		t.Fatal("member should be deleted")
	}
}

func TestRemoveDrainFailureKeepsMember(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	c, m1, w1 := readyCluster(t, svc, st, hub)
	hub.exec = func(nodeID uint64, _ string) agenthub.Result {
		if nodeID == m1 {
			return agenthub.Result{OK: false, ExitCode: 1, Err: &agent.Error{Code: "EXEC_FAILED", Message: "drain timeout"}}
		}
		return agenthub.Result{OK: true}
	}
	taskID, err := svc.RemoveNode(ctx, c.ID, w1)
	if err != nil {
		t.Fatal(err)
	}
	tk := waitTask(t, st, taskID)
	if tk.Status != store.TaskFailed {
		t.Fatalf("task: %+v", tk)
	}
	// drain 失败即停，不会去 reset 节点
	if len(hub.execs) != 1 {
		t.Fatalf("execs: %+v", hub.execs)
	}
	m, err := st.GetMember(ctx, c.ID, w1)
	if err != nil || m.Status != store.MemberFailed || !strings.Contains(m.Error, "移除失败") {
		t.Fatalf("member: %+v err=%v", m, err)
	}
	// 失败后可重试
	hub.exec = nil
	if _, err := svc.RemoveNode(ctx, c.ID, w1); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestRemoveGuards(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	c, m1, w1 := readyCluster(t, svc, st, hub)
	var ce *Error

	// 唯一 master 不能单独移除
	if _, err := svc.RemoveNode(ctx, c.ID, m1); !asErr(err, &ce) || ce.Code != "LAST_MASTER" {
		t.Fatalf("last master: %v", err)
	}
	// master 离线、worker 也离线：无处执行
	hub.online[m1], hub.online[w1] = false, false
	if _, err := svc.RemoveNode(ctx, c.ID, w1); !asErr(err, &ce) || ce.Code != "NO_ONLINE_MASTER" {
		t.Fatalf("no master: %v", err)
	}
	// 正在装的成员不能移除
	hub.online[m1], hub.online[w1] = true, true
	_ = st.SetMemberStatus(ctx, c.ID, w1, store.MemberInstalling, "")
	if _, err := svc.RemoveNode(ctx, c.ID, w1); !asErr(err, &ce) || ce.Code != "MEMBER_BUSY" {
		t.Fatalf("busy: %v", err)
	}
	// 非成员
	if _, err := svc.RemoveNode(ctx, c.ID, 999); err == nil {
		t.Fatal("non-member should fail")
	}
}

func TestDeleteCluster(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	c, m1, w1 := readyCluster(t, svc, st, hub)
	var ce *Error

	// 有成员且不 force → 拒绝
	if _, err := svc.Delete(ctx, c.ID, false); !asErr(err, &ce) || ce.Code != "CLUSTER_HAS_MEMBERS" {
		t.Fatalf("has members: %v", err)
	}
	var deleted []uint64
	svc.OnDeleted(func(id uint64) { deleted = append(deleted, id) })

	// force：w1 离线跳过，只对 m1 发 reset；记录立即删除
	hub.online[w1] = false
	ids, err := svc.Delete(ctx, c.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("task ids: %v", ids)
	}
	if _, err := st.GetCluster(ctx, c.ID); err == nil {
		t.Fatal("cluster should be gone")
	}
	if ms, _ := st.ListMembers(ctx, c.ID); len(ms) != 0 {
		t.Fatalf("members should be gone: %+v", ms)
	}
	if len(deleted) != 1 || deleted[0] != c.ID {
		t.Fatalf("onDeleted: %v", deleted)
	}
	tk := waitTask(t, st, ids[0])
	if tk.Status != store.TaskSucceeded || tk.Kind != store.TaskDestroy || tk.NodeID != m1 {
		t.Fatalf("destroy task: %+v", tk)
	}
	if len(hub.execs) != 1 || hub.execs[0].nodeID != m1 || !strings.Contains(hub.execs[0].script, "kubeadm reset") {
		t.Fatalf("execs: %+v", hub.execs)
	}

	// 空集群不 force 也能删
	c2, _ := svc.Create(ctx, CreateRequest{Name: "empty", BundleURL: "http://b"})
	if _, err := svc.Delete(ctx, c2.ID, false); err != nil {
		t.Fatal(err)
	}
}
