package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Service 精简视图。
type Service struct {
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	ClusterIP   string            `json:"clusterIp"`
	ExternalIPs []string          `json:"externalIps"`
	Ports       []ServicePort     `json:"ports"`
	Selector    map[string]string `json:"selector"`
	CreatedAt   time.Time         `json:"createdAt"`
}

// ServicePort Service 端口。
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Protocol   string `json:"protocol"`
	Port       int32  `json:"port"`
	TargetPort string `json:"targetPort"`
	NodePort   int32  `json:"nodePort,omitempty"`
}

// Ingress 精简视图。
type Ingress struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Class     string        `json:"class"`
	Rules     []IngressRule `json:"rules"`
	TLSHosts  []string      `json:"tlsHosts"`
	Addresses []string      `json:"addresses"` // 控制器回填的 LB 地址
	CreatedAt time.Time     `json:"createdAt"`
}

// IngressRule 一条 host/path → service:port。
type IngressRule struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Service string `json:"service"`
	Port    string `json:"port"`
}

// Event 事件精简视图。
type Event struct {
	Type      string    `json:"type"` // Normal | Warning
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Object    string    `json:"object"` // Kind/name
	Count     int32     `json:"count"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// Services 列 Service；namespace 为空表示全部。
func (c *Client) Services(ctx context.Context, clusterID uint64, kubeconfig, namespace string) ([]Service, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	list, err := cs.CoreV1().Services(namespace).List(tctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: 列 Service: %w", err)
	}
	out := make([]Service, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toService(&list.Items[i]))
	}
	return out, nil
}

// Ingresses 列 Ingress；namespace 为空表示全部。
func (c *Client) Ingresses(ctx context.Context, clusterID uint64, kubeconfig, namespace string) ([]Ingress, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	list, err := cs.NetworkingV1().Ingresses(namespace).List(tctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: 列 Ingress: %w", err)
	}
	out := make([]Ingress, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toIngress(&list.Items[i]))
	}
	return out, nil
}

// Events 列事件；namespace 为空表示全部；object 形如 "Pod/nginx-abc" 时只看该对象。按最近发生倒序。
func (c *Client) Events(ctx context.Context, clusterID uint64, kubeconfig, namespace, object string) ([]Event, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return nil, err
	}
	opts := metav1.ListOptions{}
	if kind, name, ok := strings.Cut(object, "/"); ok && kind != "" && name != "" {
		opts.FieldSelector = fmt.Sprintf("involvedObject.kind=%s,involvedObject.name=%s", kind, name)
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	list, err := cs.CoreV1().Events(namespace).List(tctx, opts)
	if err != nil {
		return nil, fmt.Errorf("kube: 列事件: %w", err)
	}
	out := make([]Event, 0, len(list.Items))
	for _, e := range list.Items {
		last := e.LastTimestamp.Time
		if last.IsZero() {
			last = e.EventTime.Time
		}
		if last.IsZero() {
			last = e.CreationTimestamp.Time
		}
		first := e.FirstTimestamp.Time
		if first.IsZero() {
			first = last
		}
		out = append(out, Event{
			Type: e.Type, Reason: e.Reason, Message: e.Message,
			Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
			Count:  max(e.Count, 1), FirstSeen: first, LastSeen: last,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

// ExposeRequest 为 Deployment 建 Service 的参数。
type ExposeRequest struct {
	Name       string `json:"name"`       // Service 名，空则同 Deployment
	Type       string `json:"type"`       // ClusterIP | NodePort | LoadBalancer，空为 ClusterIP
	Port       int32  `json:"port"`       // Service 端口
	TargetPort int32  `json:"targetPort"` // 容器端口，0 则同 Port
	NodePort   int32  `json:"nodePort"`   // 仅 NodePort/LoadBalancer；0 由集群分配
	Protocol   string `json:"protocol"`   // TCP | UDP，空为 TCP
}

// ExposeDeployment 为 Deployment 创建（或更新已存在的同名）Service，selector 取 Deployment 的 pod 模板标签。
func (c *Client) ExposeDeployment(ctx context.Context, clusterID uint64, kubeconfig, namespace, deployment string, req ExposeRequest) (Service, error) {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return Service{}, err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	d, err := cs.AppsV1().Deployments(namespace).Get(tctx, deployment, metav1.GetOptions{})
	if err != nil {
		return Service{}, fmt.Errorf("kube: 取 Deployment %s/%s: %w", namespace, deployment, err)
	}
	if req.Port <= 0 {
		return Service{}, fmt.Errorf("kube: port 必填")
	}
	if req.TargetPort <= 0 {
		req.TargetPort = req.Port
	}
	if req.Name == "" {
		req.Name = deployment
	}
	svcType := corev1.ServiceType(req.Type)
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}
	proto := corev1.Protocol(strings.ToUpper(req.Protocol))
	if proto == "" {
		proto = corev1.ProtocolTCP
	}
	port := corev1.ServicePort{Name: fmt.Sprintf("p%d", req.Port), Protocol: proto, Port: req.Port, TargetPort: intstr.FromInt32(req.TargetPort)}
	if req.NodePort > 0 && svcType != corev1.ServiceTypeClusterIP {
		port.NodePort = req.NodePort
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": fieldManager}},
		Spec:       corev1.ServiceSpec{Type: svcType, Selector: d.Spec.Template.Labels, Ports: []corev1.ServicePort{port}},
	}
	created, err := cs.CoreV1().Services(namespace).Create(tctx, svc, metav1.CreateOptions{FieldManager: fieldManager})
	if apierrors.IsAlreadyExists(err) {
		// 已存在则更新 spec（保留 clusterIP 等系统字段）
		existing, gerr := cs.CoreV1().Services(namespace).Get(tctx, req.Name, metav1.GetOptions{})
		if gerr != nil {
			return Service{}, fmt.Errorf("kube: 取已存在 Service: %w", gerr)
		}
		existing.Spec.Type, existing.Spec.Selector, existing.Spec.Ports = svc.Spec.Type, svc.Spec.Selector, svc.Spec.Ports
		created, err = cs.CoreV1().Services(namespace).Update(tctx, existing, metav1.UpdateOptions{FieldManager: fieldManager})
	}
	if err != nil {
		return Service{}, fmt.Errorf("kube: 建 Service %s/%s: %w", namespace, req.Name, err)
	}
	return toService(created), nil
}

// DeleteService 删 Service。
func (c *Client) DeleteService(ctx context.Context, clusterID uint64, kubeconfig, namespace, name string) error {
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return err
	}
	tctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := cs.CoreV1().Services(namespace).Delete(tctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("kube: 删 Service %s/%s: %w", namespace, name, err)
	}
	return nil
}

func toService(s *corev1.Service) Service {
	out := Service{
		Namespace: s.Namespace, Name: s.Name, Type: string(s.Spec.Type), ClusterIP: s.Spec.ClusterIP,
		ExternalIPs: append([]string{}, s.Spec.ExternalIPs...), Selector: s.Spec.Selector, CreatedAt: s.CreationTimestamp.Time,
	}
	for _, ing := range s.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			out.ExternalIPs = append(out.ExternalIPs, ing.IP)
		} else if ing.Hostname != "" {
			out.ExternalIPs = append(out.ExternalIPs, ing.Hostname)
		}
	}
	for _, p := range s.Spec.Ports {
		out.Ports = append(out.Ports, ServicePort{Name: p.Name, Protocol: string(p.Protocol), Port: p.Port, TargetPort: p.TargetPort.String(), NodePort: p.NodePort})
	}
	if out.Selector == nil {
		out.Selector = map[string]string{}
	}
	if out.Ports == nil {
		out.Ports = []ServicePort{}
	}
	return out
}

func toIngress(in *networkingv1.Ingress) Ingress {
	out := Ingress{Namespace: in.Namespace, Name: in.Name, CreatedAt: in.CreationTimestamp.Time, Rules: []IngressRule{}, TLSHosts: []string{}, Addresses: []string{}}
	if in.Spec.IngressClassName != nil {
		out.Class = *in.Spec.IngressClassName
	} else {
		out.Class = in.Annotations["kubernetes.io/ingress.class"]
	}
	for _, r := range in.Spec.Rules {
		if r.HTTP == nil {
			out.Rules = append(out.Rules, IngressRule{Host: r.Host})
			continue
		}
		for _, p := range r.HTTP.Paths {
			rule := IngressRule{Host: r.Host, Path: p.Path}
			if p.Backend.Service != nil {
				rule.Service = p.Backend.Service.Name
				if p.Backend.Service.Port.Name != "" {
					rule.Port = p.Backend.Service.Port.Name
				} else {
					rule.Port = fmt.Sprint(p.Backend.Service.Port.Number)
				}
			}
			out.Rules = append(out.Rules, rule)
		}
	}
	for _, t := range in.Spec.TLS {
		out.TLSHosts = append(out.TLSHosts, t.Hosts...)
	}
	for _, lb := range in.Status.LoadBalancer.Ingress {
		if lb.IP != "" {
			out.Addresses = append(out.Addresses, lb.IP)
		} else if lb.Hostname != "" {
			out.Addresses = append(out.Addresses, lb.Hostname)
		}
	}
	return out
}
