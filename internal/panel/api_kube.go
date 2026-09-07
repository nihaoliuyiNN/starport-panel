package panel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	"starport-panel/internal/panel/cluster"
	"starport-panel/internal/panel/kube"
)

// kubeErr 把 apiserver 错误映射成面板错误：k8s 状态错误按其 HTTP 码透传，连不上归 502。
func kubeErr(err error) error {
	var st *apierrors.StatusError
	if errors.As(err, &st) {
		code := "K8S_" + strings.ToUpper(strings.ReplaceAll(string(st.ErrStatus.Reason), " ", "_"))
		if st.ErrStatus.Reason == "" {
			code = "K8S_ERROR"
		}
		return &cluster.Error{Code: code, Message: st.ErrStatus.Message, Status: int(st.ErrStatus.Code)}
	}
	return &cluster.Error{Code: "APISERVER_UNREACHABLE", Message: err.Error(), Status: 502}
}

// kubeconfigFor 取集群 kubeconfig；失败时已写响应。
func (s *Server) kubeconfigFor(w http.ResponseWriter, r *http.Request) (uint64, string, bool) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return 0, "", false
	}
	kc, err := s.clusters.Kubeconfig(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return 0, "", false
	}
	return id, kc, true
}

func (s *Server) clusterK8sNodes(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	nodes, err := s.kube.Nodes(r.Context(), id, kc)
	if err != nil {
		writeErr(w, kubeErr(err))
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (s *Server) k8sNamespaces(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	out, err := s.kube.Namespaces(r.Context(), id, kc)
	if err != nil {
		writeErr(w, kubeErr(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// k8sPods ?namespace= 过滤，空为全部。
func (s *Server) k8sPods(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	out, err := s.kube.Pods(r.Context(), id, kc, r.URL.Query().Get("namespace"))
	if err != nil {
		writeErr(w, kubeErr(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) k8sDeployments(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	out, err := s.kube.Deployments(r.Context(), id, kc, r.URL.Query().Get("namespace"))
	if err != nil {
		writeErr(w, kubeErr(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) k8sDeletePod(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	if err := s.kube.DeletePod(r.Context(), id, kc, r.PathValue("ns"), r.PathValue("name")); err != nil {
		writeErr(w, kubeErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) k8sScale(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	var req struct {
		Replicas *int32 `json:"replicas"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Replicas == nil || *req.Replicas < 0 {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "replicas 必填且 ≥ 0", Status: 400})
		return
	}
	if err := s.kube.ScaleDeployment(r.Context(), id, kc, r.PathValue("ns"), r.PathValue("name"), *req.Replicas); err != nil {
		writeErr(w, kubeErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) k8sRestart(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	if err := s.kube.RestartDeployment(r.Context(), id, kc, r.PathValue("ns"), r.PathValue("name")); err != nil {
		writeErr(w, kubeErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// k8sPodLogs 容器日志。?container= ?tail=100 ?previous=true ?follow=true。
// 非 follow：一次性返回 text/plain。follow：分块流式输出，直到客户端断开或容器结束。
func (s *Server) k8sPodLogs(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	tail, _ := strconv.ParseInt(q.Get("tail"), 10, 64)
	if tail == 0 && q.Get("tail") == "" {
		tail = 500
	}
	opts := kube.LogOptions{
		Container: q.Get("container"),
		Follow:    q.Get("follow") == "true",
		TailLines: tail,
		Previous:  q.Get("previous") == "true",
	}
	rc, err := s.kube.PodLogs(r.Context(), id, kc, r.PathValue("ns"), r.PathValue("name"), opts)
	if err != nil {
		writeErr(w, kubeErr(err))
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if !opts.Follow {
		_, _ = io.Copy(w, rc)
		return
	}
	flusher, _ := w.(http.Flusher)
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if _, err := w.Write(append(sc.Bytes(), '\n')); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// k8sPodExec 容器 Web 终端（WebSocket），协议与节点终端一致：二进制帧=终端字节，文本帧=控制 JSON。
// ?container= ?cmd=/bin/sh（可重复给多段 argv：?cmd=sh&cmd=-c&cmd=...） ?cols= ?rows=
func (s *Server) k8sPodExec(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	cmd := q["cmd"]
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn.SetReadLimit(1 << 20)

	resize := newResizeQueue(ctx, uint16(queryInt(r, "cols", 120)), uint16(queryInt(r, "rows", 30)))
	stdinR, stdinW := io.Pipe()
	// 读浏览器 → stdin / resize
	go func() {
		defer stdinW.Close()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if _, err := stdinW.Write(data); err != nil {
					return
				}
			case websocket.MessageText:
				var msg struct {
					Type string `json:"type"`
					Cols uint16 `json:"cols"`
					Rows uint16 `json:"rows"`
				}
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
					resize.push(remotecommand.TerminalSize{Width: msg.Cols, Height: msg.Rows})
				}
			}
		}
	}()

	out := &wsWriter{conn: conn, ctx: ctx}
	execErr := s.kube.Exec(ctx, id, kc, r.PathValue("ns"), r.PathValue("name"), kube.ExecOptions{
		Container: q.Get("container"), Command: cmd, TTY: true, Stdin: stdinR, Stdout: out, Resize: resize,
	})
	exit := map[string]any{"type": "exit", "exitCode": 0}
	var codeErr utilexec.ExitError
	switch {
	case execErr == nil:
	case errors.As(execErr, &codeErr):
		exit["exitCode"] = codeErr.ExitStatus()
	default:
		exit["exitCode"] = -1
		exit["error"] = map[string]string{"code": "EXEC_FAILED", "message": execErr.Error()}
	}
	msg, _ := json.Marshal(exit)
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	_ = conn.Write(wctx, websocket.MessageText, msg)
	_ = conn.Close(websocket.StatusNormalClosure, "exec exit")
}

// wsWriter 把 stdout 写成 WebSocket 二进制帧。
type wsWriter struct {
	conn *websocket.Conn
	ctx  context.Context
}

func (w *wsWriter) Write(p []byte) (int, error) {
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	if err := w.conn.Write(ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// resizeQueue 实现 remotecommand.TerminalSizeQueue：首个尺寸立即可用，其后由浏览器 resize 推入。
type resizeQueue struct {
	ctx context.Context
	ch  chan remotecommand.TerminalSize
}

func newResizeQueue(ctx context.Context, cols, rows uint16) *resizeQueue {
	q := &resizeQueue{ctx: ctx, ch: make(chan remotecommand.TerminalSize, 8)}
	q.ch <- remotecommand.TerminalSize{Width: cols, Height: rows}
	return q
}

func (q *resizeQueue) push(sz remotecommand.TerminalSize) {
	select {
	case q.ch <- sz:
	default: // 队列满则丢弃旧的中间尺寸，只保最新
		select {
		case <-q.ch:
		default:
		}
		q.ch <- sz
	}
}

// Next 阻塞到有新尺寸；ctx 结束返回 nil 表示不再变更。
func (q *resizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case sz := <-q.ch:
		return &sz
	case <-q.ctx.Done():
		return nil
	}
}

// k8sApply server-side apply 多文档 YAML。Content-Type: application/yaml 直接给正文；
// 或 JSON {"manifest": "...", "namespace": "default"}。
func (s *Server) k8sApply(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	manifest, ns, ok := readManifest(w, r)
	if !ok {
		return
	}
	applied, err := s.kube.Apply(r.Context(), id, kc, ns, manifest)
	if err != nil {
		writeJSON(w, kubeStatus(err), map[string]any{"applied": nonNil(applied), "error": errBody(kubeErr(err))})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": nonNil(applied)})
}

// k8sDeleteManifest 按 YAML 删除对象。
func (s *Server) k8sDeleteManifest(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	manifest, ns, ok := readManifest(w, r)
	if !ok {
		return
	}
	deleted, err := s.kube.DeleteManifest(r.Context(), id, kc, ns, manifest)
	if err != nil {
		writeJSON(w, kubeStatus(err), map[string]any{"deleted": nonNil(deleted), "error": errBody(kubeErr(err))})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": nonNil(deleted)})
}

func readManifest(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "读取请求体失败: " + err.Error(), Status: 400})
		return nil, "", false
	}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var req struct {
			Manifest  string `json:"manifest"`
			Namespace string `json:"namespace"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "请求体不是合法 JSON: " + err.Error(), Status: 400})
			return nil, "", false
		}
		if strings.TrimSpace(req.Manifest) == "" {
			writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "manifest 必填", Status: 400})
			return nil, "", false
		}
		return []byte(req.Manifest), req.Namespace, true
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "manifest 为空", Status: 400})
		return nil, "", false
	}
	return body, r.URL.Query().Get("namespace"), true
}

func kubeStatus(err error) int {
	var ce *cluster.Error
	if errors.As(kubeErr(err), &ce) {
		return ce.Status
	}
	return http.StatusBadGateway
}

func errBody(err error) map[string]string {
	var ce *cluster.Error
	if errors.As(err, &ce) {
		return map[string]string{"code": ce.Code, "message": ce.Message}
	}
	return map[string]string{"code": "INTERNAL", "message": err.Error()}
}

func nonNil(v []kube.Applied) []kube.Applied {
	if v == nil {
		return []kube.Applied{}
	}
	return v
}
