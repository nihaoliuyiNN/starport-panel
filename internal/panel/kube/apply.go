package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"
)

// fieldManager server-side apply 的字段归属者。
const fieldManager = "starport-panel"

// Applied 一个已 apply 的对象。
type Applied struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Apply 对多文档 YAML 做 server-side apply（等价 kubectl apply --server-side --force-conflicts）。
// 无 namespace 的 namespaced 资源落到 defaultNamespace（空则 default）。任一文档失败即返回，已 apply 的不回滚。
func (c *Client) Apply(ctx context.Context, clusterID uint64, kubeconfig, defaultNamespace string, manifest []byte) ([]Applied, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	if defaultNamespace == "" {
		defaultNamespace = "default"
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(e.disc)

	var out []Applied
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return out, fmt.Errorf("kube: 解析 YAML: %w", err)
		}
		if len(raw) == 0 {
			continue // 空文档（--- 分隔产生）
		}
		obj := &unstructured.Unstructured{Object: raw}
		gvk := obj.GroupVersionKind()
		if gvk.Kind == "" || obj.GetName() == "" {
			return out, fmt.Errorf("kube: 文档缺少 kind 或 metadata.name")
		}
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
		if err != nil {
			e.disc.Invalidate()
			if mapping, err = mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version); err != nil {
				return out, fmt.Errorf("kube: 未知资源 %s: %w", gvk, err)
			}
		}
		ns := ""
		if mapping.Scope.Name() == "namespace" {
			ns = obj.GetNamespace()
			if ns == "" {
				ns = defaultNamespace
				obj.SetNamespace(ns)
			}
		}
		data, err := yaml.Marshal(obj.Object)
		if err != nil {
			return out, err
		}
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = e.dyn.Resource(mapping.Resource).Namespace(ns).Patch(tctx, obj.GetName(), types.ApplyPatchType, data,
			metav1.PatchOptions{FieldManager: fieldManager, Force: ptr(true)})
		cancel()
		if err != nil {
			return out, fmt.Errorf("kube: apply %s %s/%s: %w", gvk.Kind, ns, obj.GetName(), err)
		}
		out = append(out, Applied{Kind: gvk.Kind, Namespace: ns, Name: obj.GetName()})
	}
	return out, nil
}

// DeleteManifest 按多文档 YAML 删除对象（等价 kubectl delete -f）。不存在的忽略。
func (c *Client) DeleteManifest(ctx context.Context, clusterID uint64, kubeconfig, defaultNamespace string, manifest []byte) ([]Applied, error) {
	e, err := c.entry(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	if defaultNamespace == "" {
		defaultNamespace = "default"
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(e.disc)
	var out []Applied
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return out, fmt.Errorf("kube: 解析 YAML: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{Object: raw}
		gvk := obj.GroupVersionKind()
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
		if err != nil {
			return out, fmt.Errorf("kube: 未知资源 %s: %w", gvk, err)
		}
		ns := ""
		if mapping.Scope.Name() == "namespace" {
			ns = obj.GetNamespace()
			if ns == "" {
				ns = defaultNamespace
			}
		}
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = e.dyn.Resource(mapping.Resource).Namespace(ns).Delete(tctx, obj.GetName(), metav1.DeleteOptions{})
		cancel()
		if err != nil && !isNotFound(err) {
			return out, fmt.Errorf("kube: 删除 %s %s/%s: %w", gvk.Kind, ns, obj.GetName(), err)
		}
		out = append(out, Applied{Kind: gvk.Kind, Namespace: ns, Name: obj.GetName()})
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

func isNotFound(err error) bool { return apierrors.IsNotFound(err) }
