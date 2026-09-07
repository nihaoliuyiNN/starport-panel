package cluster

import (
	"context"
	"testing"

	"starport-panel/internal/agent"
	"starport-panel/internal/installer"
	"starport-panel/internal/panel/store"
)

func TestImportCluster(t *testing.T) {
	svc, st, hub := newEnv(t)
	ctx := context.Background()
	var ce *Error

	if _, err := svc.Import(ctx, ImportRequest{Name: "", Kubeconfig: "kc"}); !asErr(err, &ce) || ce.Code != "INVALID_ARGUMENT" {
		t.Fatalf("no name: %v", err)
	}
	if _, err := svc.Import(ctx, ImportRequest{Name: "ext", Kubeconfig: " "}); !asErr(err, &ce) || ce.Code != "INVALID_ARGUMENT" {
		t.Fatalf("no kubeconfig: %v", err)
	}

	c, err := svc.Import(ctx, ImportRequest{Name: "ext", Kubeconfig: "apiVersion: v1", K8sVersion: "v1.33.0", Endpoint: "https://10.0.0.5:6443", PodCIDR: "10.244.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != store.ClusterReady || c.Source != store.SourceImported || c.ControlPlaneEndpoint != "10.0.0.5:6443" || c.K8sVersion != "v1.33.0" {
		t.Fatalf("imported: %+v", c)
	}
	// 重名
	if _, err := svc.Import(ctx, ImportRequest{Name: "ext", Kubeconfig: "x"}); !asErr(err, &ce) || ce.Code != "CLUSTER_NAME_EXISTS" {
		t.Fatalf("dup: %v", err)
	}
	// kubeconfig 立即可取
	kc, err := svc.Kubeconfig(ctx, c.ID)
	if err != nil || kc != "apiVersion: v1" {
		t.Fatalf("kubeconfig: %q %v", kc, err)
	}
	// 不能加节点
	n, _, _ := st.RegisterNode(agent.Facts{Hostname: "n", InternalIP: "10.0.0.9"}, "v1")
	hub.online[n] = true
	if _, err := svc.AddNode(ctx, c.ID, n, installer.RoleWorker); !asErr(err, &ce) || ce.Code != "CLUSTER_IMPORTED" {
		t.Fatalf("add node to imported: %v", err)
	}
	// 替换 kubeconfig：只允许接管集群
	if err := svc.UpdateKubeconfig(ctx, c.ID, "apiVersion: v1 #2"); err != nil {
		t.Fatal(err)
	}
	kc, _ = svc.Kubeconfig(ctx, c.ID)
	if kc != "apiVersion: v1 #2" {
		t.Fatalf("updated kubeconfig: %q", kc)
	}
	k, _ := svc.Create(ctx, CreateRequest{Name: "kubeadm", BundleURL: "http://b"})
	if err := svc.UpdateKubeconfig(ctx, k.ID, "x"); !asErr(err, &ce) || ce.Code != "CLUSTER_NOT_IMPORTED" {
		t.Fatalf("update kubeadm cluster kubeconfig: %v", err)
	}
	// 删除：无成员，直接删记录
	if _, err := svc.Delete(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCluster(ctx, c.ID); err != store.ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	// 老库里没有 source 列的旧集群：Create 走默认 kubeadm
	if k.Source != store.SourceKubeadm {
		t.Fatalf("source default: %q", k.Source)
	}
}
