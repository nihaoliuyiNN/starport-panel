package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// ErrNoMetricsServer 集群没装 metrics-server（metrics.k8s.io 不可用）。
var ErrNoMetricsServer = errors.New("kube: 集群未安装 metrics-server（metrics.k8s.io 不可用）")

// NodeUsage 节点即时用量（来自 metrics-server，约 15s 采样一次）。
type NodeUsage struct {
	Name          string    `json:"name"`
	CPUMilli      int64     `json:"cpuMilli"`    // 已用 CPU（毫核）
	CPUCapMilli   int64     `json:"cpuCapMilli"` // 可分配 CPU（毫核）
	MemBytes      int64     `json:"memBytes"`    // 已用内存
	MemCapBytes   int64     `json:"memCapBytes"` // 可分配内存
	CPUPercent    float64   `json:"cpuPercent"`  // 0-100
	MemPercent    float64   `json:"memPercent"`  // 0-100
	Timestamp     time.Time `json:"timestamp"`
	WindowSeconds int64     `json:"windowSeconds"`
}

// PodUsage Pod 即时用量（各容器之和）。
type PodUsage struct {
	Namespace  string           `json:"namespace"`
	Name       string           `json:"name"`
	CPUMilli   int64            `json:"cpuMilli"`
	MemBytes   int64            `json:"memBytes"`
	Timestamp  time.Time        `json:"timestamp"`
	Containers []ContainerUsage `json:"containers"`
}

// ContainerUsage 单容器用量。
type ContainerUsage struct {
	Name     string `json:"name"`
	CPUMilli int64  `json:"cpuMilli"`
	MemBytes int64  `json:"memBytes"`
}

// NodeMetrics 全部节点用量；与节点 allocatable 合并算百分比。
func (c *Client) NodeMetrics(ctx context.Context, clusterID uint64, kubeconfig string) ([]NodeUsage, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	mc, err := metricsclient.NewForConfig(e.cfg)
	if err != nil {
		return nil, fmt.Errorf("kube: 建 metrics 客户端: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list, err := mc.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, metricsErr(err)
	}
	nodes, err := e.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: 列节点: %w", err)
	}
	capacity := map[string][2]int64{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		capacity[n.Name] = [2]int64{n.Status.Allocatable.Cpu().MilliValue(), n.Status.Allocatable.Memory().Value()}
	}
	out := make([]NodeUsage, 0, len(list.Items))
	for i := range list.Items {
		m := &list.Items[i]
		u := NodeUsage{
			Name:          m.Name,
			CPUMilli:      m.Usage.Cpu().MilliValue(),
			MemBytes:      m.Usage.Memory().Value(),
			Timestamp:     m.Timestamp.Time,
			WindowSeconds: int64(m.Window.Duration / time.Second),
		}
		if c, ok := capacity[m.Name]; ok {
			u.CPUCapMilli, u.MemCapBytes = c[0], c[1]
			u.CPUPercent = pct(u.CPUMilli, c[0])
			u.MemPercent = pct(u.MemBytes, c[1])
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PodMetrics 某命名空间（空为全部）的 Pod 用量。
func (c *Client) PodMetrics(ctx context.Context, clusterID uint64, kubeconfig, namespace string) ([]PodUsage, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	mc, err := metricsclient.NewForConfig(e.cfg)
	if err != nil {
		return nil, fmt.Errorf("kube: 建 metrics 客户端: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list, err := mc.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, metricsErr(err)
	}
	out := make([]PodUsage, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toPodUsage(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func toPodUsage(m *metricsv1beta1.PodMetrics) PodUsage {
	u := PodUsage{Namespace: m.Namespace, Name: m.Name, Timestamp: m.Timestamp.Time, Containers: make([]ContainerUsage, 0, len(m.Containers))}
	for _, c := range m.Containers {
		cu := ContainerUsage{Name: c.Name, CPUMilli: c.Usage.Cpu().MilliValue(), MemBytes: c.Usage.Memory().Value()}
		u.CPUMilli += cu.CPUMilli
		u.MemBytes += cu.MemBytes
		u.Containers = append(u.Containers, cu)
	}
	return u
}

// metricsErr metrics.k8s.io 组不存在（404 / NotFound / 服务不可用）→ ErrNoMetricsServer。
func metricsErr(err error) error {
	if apierrors.IsNotFound(err) || apierrors.IsServiceUnavailable(err) {
		return ErrNoMetricsServer
	}
	var st *apierrors.StatusError
	if errors.As(err, &st) {
		return err
	}
	// discovery 找不到组时 client-go 返回的不是 StatusError，而是 "the server could not find the requested resource"
	return fmt.Errorf("kube: 读取用量: %w", err)
}

func pct(used, capacity int64) float64 {
	if capacity <= 0 {
		return 0
	}
	p := float64(used) / float64(capacity) * 100
	if p > 100 {
		p = 100
	}
	return float64(int(p*10)) / 10
}
