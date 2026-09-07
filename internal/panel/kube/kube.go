// Package kube 是面板经 kubeconfig 直连集群 apiserver 的只读视图（client-go）。
// clientset 按集群缓存，kubeconfig 变化（重装 / 轮换）时自动重建。
package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NodeInfo 集群内节点的精简视图。
type NodeInfo struct {
	Name           string    `json:"name"`
	Ready          bool      `json:"ready"`
	Unschedulable  bool      `json:"unschedulable"`
	Roles          []string  `json:"roles"`
	InternalIP     string    `json:"internalIp"`
	KubeletVersion string    `json:"kubeletVersion"`
	OSImage        string    `json:"osImage"`
	Kernel         string    `json:"kernel"`
	Runtime        string    `json:"containerRuntime"`
	CPU            string    `json:"cpu"`
	Memory         string    `json:"memory"`
	Pods           string    `json:"pods"`
	CreatedAt      time.Time `json:"createdAt"`
}

// Client 见包注释。
type Client struct {
	mu    sync.Mutex
	cache map[uint64]cached
}

type cached struct {
	hash string
	cfg  *rest.Config
	cs   *kubernetes.Clientset
	dyn  *dynamic.DynamicClient
	disc discovery.CachedDiscoveryInterface
}

// New 建客户端缓存。
func New() *Client { return &Client{cache: make(map[uint64]cached)} }

// Nodes 列出集群节点。
func (c *Client) Nodes(ctx context.Context, clusterID uint64, kubeconfig string) ([]NodeInfo, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: 列节点: %w", err)
	}
	out := make([]NodeInfo, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toInfo(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Forget 丢弃某集群缓存（集群删除时）。
func (c *Client) Forget(clusterID uint64) {
	c.mu.Lock()
	delete(c.cache, clusterID)
	c.mu.Unlock()
}

func (c *Client) clientset(clusterID uint64, kubeconfig string) (*kubernetes.Clientset, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	return e.cs, nil
}

func (c *Client) restConfig(clusterID uint64, kubeconfig string) (*rest.Config, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	return e.cfg, nil
}

// entry 取（或按 kubeconfig 内容哈希重建）某集群的客户端组。
func (c *Client) entry(clusterID uint64, kubeconfig string) (cached, error) {
	sum := sha256.Sum256([]byte(kubeconfig))
	hash := hex.EncodeToString(sum[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.cache[clusterID]; ok && e.hash == hash {
		return e, nil
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return cached{}, fmt.Errorf("kube: 解析 kubeconfig: %w", err)
	}
	// 不设全局 Timeout：日志 follow / exec 是长流，由各调用的 ctx 控制
	cfg.UserAgent = "starport-panel"
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return cached{}, fmt.Errorf("kube: 建客户端: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return cached{}, fmt.Errorf("kube: 建 dynamic 客户端: %w", err)
	}
	disc := memory.NewMemCacheClient(cs.Discovery())
	e := cached{hash: hash, cfg: cfg, cs: cs, dyn: dyn, disc: disc}
	c.cache[clusterID] = e
	return e, nil
}

func toInfo(n *corev1.Node) NodeInfo {
	info := NodeInfo{
		Name:           n.Name,
		Unschedulable:  n.Spec.Unschedulable,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		OSImage:        n.Status.NodeInfo.OSImage,
		Kernel:         n.Status.NodeInfo.KernelVersion,
		Runtime:        n.Status.NodeInfo.ContainerRuntimeVersion,
		CPU:            n.Status.Capacity.Cpu().String(),
		Memory:         n.Status.Capacity.Memory().String(),
		Pods:           n.Status.Capacity.Pods().String(),
		CreatedAt:      n.CreationTimestamp.Time,
		Roles:          []string{},
	}
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			info.Ready = cond.Status == corev1.ConditionTrue
		}
	}
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			info.InternalIP = a.Address
		}
	}
	for k := range n.Labels {
		if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
			info.Roles = append(info.Roles, r)
		}
	}
	sort.Strings(info.Roles)
	return info
}
