package kube

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// ResourceKind 集群里可用的一种资源（discovery 结果）。
type ResourceKind struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Resource   string   `json:"resource"` // 复数名，如 deployments
	Kind       string   `json:"kind"`
	Namespaced bool     `json:"namespaced"`
	Verbs      []string `json:"verbs"`
}

// ResourceItem 通用列表项：只带元数据，正文经 GetResource 取。
type ResourceItem struct {
	Namespace string            `json:"namespace,omitempty"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	CreatedAt time.Time         `json:"createdAt"`
}

// ResourceKinds 列出集群支持的全部资源类型（含 CRD），仅保留可 list 的、非子资源。
func (c *Client) ResourceKinds(ctx context.Context, clusterID uint64, kubeconfig string) ([]ResourceKind, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	_, lists, err := e.disc.ServerGroupsAndResources()
	if err != nil && len(lists) == 0 {
		return nil, fmt.Errorf("kube: discovery: %w", err)
	}
	var out []ResourceKind
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") || !slices.Contains(r.Verbs, "list") {
				continue
			}
			out = append(out, ResourceKind{Group: gv.Group, Version: gv.Version, Resource: r.Name, Kind: r.Kind, Namespaced: r.Namespaced, Verbs: r.Verbs})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Resource < out[j].Resource
	})
	return out, nil
}

// ListResources 通用列表。group 为空表示 core（如 group="" version="v1" resource="configmaps"）；
// namespace 为空表示全部（集群级资源忽略）。labelSelector 可选。
func (c *Client) ListResources(ctx context.Context, clusterID uint64, kubeconfig string, gvr schema.GroupVersionResource, namespace, labelSelector string) ([]ResourceItem, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	list, err := e.dyn.Resource(gvr).Namespace(namespace).List(tctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("kube: 列 %s: %w", gvr.Resource, err)
	}
	out := make([]ResourceItem, 0, len(list.Items))
	for _, it := range list.Items {
		labels := it.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		out = append(out, ResourceItem{Namespace: it.GetNamespace(), Name: it.GetName(), Labels: labels, CreatedAt: it.GetCreationTimestamp().Time})
	}
	return out, nil
}

// GetResource 取单个对象，返回 YAML（去掉 managedFields 噪音）。集群级资源 namespace 传空。
func (c *Client) GetResource(ctx context.Context, clusterID uint64, kubeconfig string, gvr schema.GroupVersionResource, namespace, name string) ([]byte, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	obj, err := e.dyn.Resource(gvr).Namespace(namespace).Get(tctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: 取 %s %s/%s: %w", gvr.Resource, namespace, name, err)
	}
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	return yaml.Marshal(obj.Object)
}

// DeleteResource 删单个对象。
func (c *Client) DeleteResource(ctx context.Context, clusterID uint64, kubeconfig string, gvr schema.GroupVersionResource, namespace, name string) error {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := e.dyn.Resource(gvr).Namespace(namespace).Delete(tctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("kube: 删 %s %s/%s: %w", gvr.Resource, namespace, name, err)
	}
	return nil
}
