package kube

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Namespace 命名空间精简视图。
type Namespace struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// Pod 精简视图。
type Pod struct {
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	Phase      string      `json:"phase"`
	Ready      string      `json:"ready"` // 就绪容器数/总数，如 1/2
	Restarts   int32       `json:"restarts"`
	Node       string      `json:"node"`
	PodIP      string      `json:"podIp"`
	Containers []Container `json:"containers"`
	CreatedAt  time.Time   `json:"createdAt"`
}

// Container Pod 内容器。
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Ready bool   `json:"ready"`
	State string `json:"state"` // running | waiting:<reason> | terminated:<reason>
}

// Deployment 精简视图。
type Deployment struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Replicas  int32             `json:"replicas"`
	Ready     int32             `json:"ready"`
	Updated   int32             `json:"updated"`
	Available int32             `json:"available"`
	Images    []string          `json:"images"`
	Labels    map[string]string `json:"labels"`
	CreatedAt time.Time         `json:"createdAt"`
}

// Namespaces 列命名空间。
func (c *Client) Namespaces(ctx context.Context, clusterID uint64, kubeconfig string) ([]Namespace, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	list, err := cs.CoreV1().Namespaces().List(tctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: 列命名空间: %w", err)
	}
	out := make([]Namespace, 0, len(list.Items))
	for _, ns := range list.Items {
		out = append(out, Namespace{Name: ns.Name, Status: string(ns.Status.Phase), CreatedAt: ns.CreationTimestamp.Time})
	}
	return out, nil
}

// Pods 列 Pod；namespace 为空表示全部。
func (c *Client) Pods(ctx context.Context, clusterID uint64, kubeconfig, namespace string) ([]Pod, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	list, err := cs.CoreV1().Pods(namespace).List(tctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: 列 Pod: %w", err)
	}
	out := make([]Pod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toPod(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Deployments 列 Deployment；namespace 为空表示全部。
func (c *Client) Deployments(ctx context.Context, clusterID uint64, kubeconfig, namespace string) ([]Deployment, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	list, err := cs.AppsV1().Deployments(namespace).List(tctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: 列 Deployment: %w", err)
	}
	out := make([]Deployment, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toDeployment(&list.Items[i]))
	}
	return out, nil
}

// ScaleDeployment 调副本数。
func (c *Client) ScaleDeployment(ctx context.Context, clusterID uint64, kubeconfig, namespace, name string, replicas int32) error {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	_, err = cs.AppsV1().Deployments(namespace).Patch(tctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("kube: 扩缩 %s/%s: %w", namespace, name, err)
	}
	return nil
}

// RestartDeployment 滚动重启（与 kubectl rollout restart 同法：改 pod template 注解）。
func (c *Client) RestartDeployment(ctx context.Context, clusterID uint64, kubeconfig, namespace, name string) error {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	_, err = cs.AppsV1().Deployments(namespace).Patch(tctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("kube: 重启 %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeletePod 删 Pod（由控制器重建）。
func (c *Client) DeletePod(ctx context.Context, clusterID uint64, kubeconfig, namespace, name string) error {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := cs.CoreV1().Pods(namespace).Delete(tctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("kube: 删 Pod %s/%s: %w", namespace, name, err)
	}
	return nil
}

// LogOptions 容器日志参数。
type LogOptions struct {
	Container string
	Follow    bool
	TailLines int64 // <=0 表示全部
	Previous  bool
}

// PodLogs 打开容器日志流；调用方负责 Close。follow 时流随 ctx 取消而结束。
func (c *Client) PodLogs(ctx context.Context, clusterID uint64, kubeconfig, namespace, name string, opts LogOptions) (io.ReadCloser, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	o := &corev1.PodLogOptions{Container: opts.Container, Follow: opts.Follow, Previous: opts.Previous, Timestamps: false}
	if opts.TailLines > 0 {
		o.TailLines = &opts.TailLines
	}
	rc, err := cs.CoreV1().Pods(namespace).GetLogs(name, o).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("kube: 取日志 %s/%s: %w", namespace, name, err)
	}
	return rc, nil
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 20*time.Second)
}

func toPod(p *corev1.Pod) Pod {
	out := Pod{
		Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase),
		Node: p.Spec.NodeName, PodIP: p.Status.PodIP, CreatedAt: p.CreationTimestamp.Time,
	}
	ready := 0
	status := map[string]corev1.ContainerStatus{}
	for _, cs := range p.Status.ContainerStatuses {
		status[cs.Name] = cs
		out.Restarts += cs.RestartCount
		if cs.Ready {
			ready++
		}
	}
	for _, ct := range p.Spec.Containers {
		c := Container{Name: ct.Name, Image: ct.Image}
		if st, ok := status[ct.Name]; ok {
			c.Ready = st.Ready
			switch {
			case st.State.Running != nil:
				c.State = "running"
			case st.State.Waiting != nil:
				c.State = "waiting:" + st.State.Waiting.Reason
			case st.State.Terminated != nil:
				c.State = "terminated:" + st.State.Terminated.Reason
			}
		}
		out.Containers = append(out.Containers, c)
	}
	out.Ready = fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers))
	if p.DeletionTimestamp != nil {
		out.Phase = "Terminating"
	}
	return out
}

func toDeployment(d *appsv1.Deployment) Deployment {
	out := Deployment{
		Namespace: d.Namespace, Name: d.Name,
		Ready: d.Status.ReadyReplicas, Updated: d.Status.UpdatedReplicas, Available: d.Status.AvailableReplicas,
		Labels: d.Labels, CreatedAt: d.CreationTimestamp.Time,
	}
	if d.Spec.Replicas != nil {
		out.Replicas = *d.Spec.Replicas
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		out.Images = append(out.Images, c.Image)
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	return out
}
