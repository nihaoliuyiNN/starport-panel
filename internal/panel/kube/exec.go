package kube

import (
	"context"
	"fmt"
	"io"
	"net/url"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// ExecOptions 容器 exec 参数。
type ExecOptions struct {
	Container string
	Command   []string
	TTY       bool
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer // TTY 模式下忽略（合并进 stdout）
	Resize    remotecommand.TerminalSizeQueue
}

// Exec 在容器里执行命令并把流接到 opts；阻塞到进程退出或 ctx 取消。
// 传输优先 WebSocket（k8s ≥1.29 默认开启），失败回落 SPDY。
func (c *Client) Exec(ctx context.Context, clusterID uint64, kubeconfig, namespace, pod string, opts ExecOptions) error {
	cfg, err := c.restConfig(clusterID, kubeconfig)
	if err != nil {
		return err
	}
	cs, err := c.clientset(clusterID, kubeconfig)
	if err != nil {
		return err
	}
	if len(opts.Command) == 0 {
		opts.Command = []string{"/bin/sh"}
	}
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: opts.Container,
			Command:   opts.Command,
			Stdin:     opts.Stdin != nil,
			Stdout:    true,
			Stderr:    !opts.TTY,
			TTY:       opts.TTY,
		}, runtime.NewParameterCodec(scheme.Scheme))

	exec, err := newExecutor(cfg, req.URL().String())
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: opts.Stdin, Stdout: opts.Stdout, Stderr: opts.Stderr, Tty: opts.TTY, TerminalSizeQueue: opts.Resize,
	})
}

// newExecutor 与 kubectl 同策略：WebSocket 优先，升级失败（老 apiserver / 中间代理不支持）回落 SPDY。
func newExecutor(cfg *rest.Config, rawURL string) (remotecommand.Executor, error) {
	ws, err := remotecommand.NewWebSocketExecutor(cfg, "GET", rawURL)
	if err != nil {
		return nil, fmt.Errorf("kube: 建 websocket executor: %w", err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	spdy, err := remotecommand.NewSPDYExecutor(cfg, "POST", u)
	if err != nil {
		return nil, fmt.Errorf("kube: 建 spdy executor: %w", err)
	}
	return remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
}
