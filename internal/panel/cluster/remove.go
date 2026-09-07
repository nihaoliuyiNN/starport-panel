package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	"starport-panel/internal/installer"
	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/store"
)

// drainScript 在 master 上把节点从集群摘除：先 drain（超时不阻塞整个流程），再 delete node。
// 节点名按 kubeadm 默认 = 小写主机名；找不到（从未 join 成功）视为已摘除。
const drainScript = `set -uo pipefail
export KUBECONFIG=/etc/kubernetes/admin.conf
NODE=%q
if ! kubectl get node "$NODE" >/dev/null 2>&1; then
  echo "节点 $NODE 不在集群中，跳过 drain"
  exit 0
fi
kubectl drain "$NODE" --ignore-daemonsets --delete-emptydir-data --force --timeout=180s || echo "drain 超时/失败，继续删除节点对象"
kubectl delete node "$NODE" --timeout=60s
`

// resetScript 在被移除的节点上清干净 kubeadm/容器运行时残留，使其可重新纳管。
const resetScript = `set -uo pipefail
kubeadm reset -f --cri-socket unix:///run/containerd/containerd.sock || kubeadm reset -f || true
systemctl stop kubelet 2>/dev/null || true
rm -rf /etc/kubernetes /var/lib/kubelet /var/lib/etcd /etc/cni/net.d /var/lib/cni /run/flannel /var/lib/cilium /var/run/calico
rm -f /etc/kubernetes/manifests/kube-vip.yaml
iptables -F 2>/dev/null; iptables -t nat -F 2>/dev/null; iptables -t mangle -F 2>/dev/null; iptables -X 2>/dev/null
ipvsadm --clear 2>/dev/null || true
ip link delete cilium_host 2>/dev/null; ip link delete cilium_net 2>/dev/null; ip link delete cilium_vxlan 2>/dev/null
ip link delete vxlan.calico 2>/dev/null; ip link delete cni0 2>/dev/null; ip link delete flannel.1 2>/dev/null
systemctl restart containerd 2>/dev/null || true
echo "节点已重置"
`

// RemoveNode 把节点移出集群：在一台其它在线 master 上 drain + delete node，再在该节点上 kubeadm reset，
// 最后删成员记录。返回任务 ID。首/唯一 master 不能单独移除——请删集群。
func (s *Service) RemoveNode(ctx context.Context, clusterID, nodeID uint64) (uint64, error) {
	c, err := s.store.GetCluster(ctx, clusterID)
	if err != nil {
		return 0, err
	}
	m, err := s.store.GetMember(ctx, clusterID, nodeID)
	if err != nil {
		return 0, err
	}
	if m.Status == store.MemberInstalling || m.Status == store.MemberRemoving {
		return 0, conflict("MEMBER_BUSY", "节点 %d 正在 %s，请等任务结束", nodeID, m.Status)
	}
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return 0, err
	}

	// 找一台「不是本节点」的在线 master 来 drain；集群从未就绪（首 master 装失败）则无需 drain
	var masterID uint64
	if c.Status == store.ClusterReady {
		masterID, err = s.readyMasterExcept(ctx, clusterID, nodeID)
		if err != nil {
			if isMaster(m.Role) {
				return 0, conflict("LAST_MASTER", "节点 %d 是集群唯一的控制面，不能单独移除；请删除集群", nodeID)
			}
			return 0, err
		}
	}
	if !s.hub.Online(nodeID) && masterID == 0 {
		return 0, conflict("NODE_OFFLINE", "节点 %d 不在线，且无在线 master 可执行摘除", nodeID)
	}

	if err := s.store.SetMemberStatus(ctx, clusterID, nodeID, store.MemberRemoving, ""); err != nil {
		return 0, err
	}
	nodeName := strings.ToLower(node.Facts.Hostname)
	nodeOnline := s.hub.Online(nodeID)
	taskID, err := s.tasks.Start(store.TaskRemove, nodeID, clusterID,
		func(ctx context.Context, logf func(string)) (agenthub.Result, error) {
			if masterID != 0 {
				logf(fmt.Sprintf("在 master 节点 %d 上 drain 并删除 %s", masterID, nodeName))
				res, err := s.hub.Exec(ctx, masterID, fmt.Sprintf(drainScript, nodeName), 6*time.Minute, logf)
				if err != nil {
					return res, err
				}
				if !res.OK {
					return res, nil
				}
			}
			if !nodeOnline {
				logf("节点不在线，跳过本机 kubeadm reset（下次纳管前请手动重置）")
				return agenthub.Result{OK: true}, nil
			}
			logf("在节点上执行 kubeadm reset")
			return s.hub.Exec(ctx, nodeID, resetScript, 5*time.Minute, logf)
		},
		func(ctx context.Context, t store.Task, res agenthub.Result) {
			if res.OK {
				_ = s.store.DeleteMember(ctx, t.ClusterID, t.NodeID)
				return
			}
			msg := t.ErrorMessage
			if msg == "" {
				msg = "移除失败"
			}
			_ = s.store.SetMemberStatus(ctx, t.ClusterID, t.NodeID, store.MemberFailed, "移除失败: "+msg)
		})
	if err != nil {
		_ = s.store.SetMemberStatus(ctx, clusterID, nodeID, m.Status, m.Error)
		return 0, err
	}
	_ = s.store.SetMemberTask(ctx, clusterID, nodeID, taskID)
	return taskID, nil
}

// Delete 删集群。force=false 要求已无成员；force=true 对每个在线成员发 kubeadm reset（各自一个任务，
// 不等结果），随后删除记录。返回发起的任务 ID 列表。
func (s *Service) Delete(ctx context.Context, clusterID uint64, force bool) ([]uint64, error) {
	if _, err := s.store.GetCluster(ctx, clusterID); err != nil {
		return nil, err
	}
	ms, err := s.store.ListMembers(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if len(ms) > 0 && !force {
		return nil, conflict("CLUSTER_HAS_MEMBERS", "集群仍有 %d 个节点；先逐个移除，或 ?force=true 强制重置全部节点", len(ms))
	}
	var taskIDs []uint64
	for _, m := range ms {
		if m.Status == store.MemberInstalling || m.Status == store.MemberRemoving {
			return taskIDs, conflict("MEMBER_BUSY", "节点 %d 正在 %s，请等任务结束", m.NodeID, m.Status)
		}
		if !s.hub.Online(m.NodeID) {
			continue
		}
		nodeID := m.NodeID
		id, err := s.tasks.Start(store.TaskDestroy, nodeID, clusterID,
			func(ctx context.Context, logf func(string)) (agenthub.Result, error) {
				logf("删集群：在节点上执行 kubeadm reset")
				return s.hub.Exec(ctx, nodeID, resetScript, 5*time.Minute, logf)
			}, nil)
		if err != nil {
			return taskIDs, err
		}
		taskIDs = append(taskIDs, id)
	}
	if err := s.store.DeleteCluster(ctx, clusterID); err != nil {
		return taskIDs, err
	}
	if s.onDeleted != nil {
		s.onDeleted(clusterID)
	}
	return taskIDs, nil
}

// OnDeleted 注册集群删除后的回调（如丢弃 kube 客户端缓存）。
func (s *Service) OnDeleted(fn func(clusterID uint64)) { s.onDeleted = fn }

// readyMasterExcept 找一台在线 ready 且不是 except 的控制面节点。
func (s *Service) readyMasterExcept(ctx context.Context, clusterID, except uint64) (uint64, error) {
	ms, err := s.store.ListMembers(ctx, clusterID)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if m.NodeID != except && m.Status == store.MemberReady && isMaster(m.Role) && s.hub.Online(m.NodeID) {
			return m.NodeID, nil
		}
	}
	return 0, conflict("NO_ONLINE_MASTER", "没有其它在线的控制面节点可执行摘除")
}

func isMaster(role string) bool {
	return role == installer.RoleFirstMaster || role == installer.RoleJoinMaster
}
