package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/cluster"
	"starport-panel/internal/panel/kube"
	"starport-panel/internal/panel/store"
	"starport-panel/internal/panel/task"
	"starport-panel/internal/panel/ui"
)

// routes 面板 HTTP API（/api/v1）。响应一律 JSON；错误形如 {"error":{"code","message"}}。
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": s.cfg.Version})
	})
	mux.HandleFunc("GET /api/v1/openapi.yaml", s.openapi)

	// agent 引导注册；enroll 给 UI 生成纳管命令（需 API 令牌）
	mux.Handle("POST "+agenthub.RegisterPath, s.hub.RegisterHandler())
	mux.HandleFunc("GET /api/v1/agents/enroll", s.agentEnroll)

	// 节点
	mux.HandleFunc("GET /api/v1/nodes", s.listNodes)
	mux.HandleFunc("GET /api/v1/nodes/{id}", s.getNode)
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.deleteNode)
	mux.HandleFunc("POST /api/v1/nodes/{id}/exec", s.execNode)
	mux.HandleFunc("GET /api/v1/nodes/{id}/terminal", s.nodeTerminal) // WebSocket
	mux.HandleFunc("POST /api/v1/nodes/upgrade", s.upgradeNodes)      // 批量升级 agent
	mux.HandleFunc("POST /api/v1/nodes/{id}/upgrade", s.upgradeNode)

	// 集群
	mux.HandleFunc("GET /api/v1/clusters", s.listClusters)
	mux.HandleFunc("POST /api/v1/clusters", s.createCluster)
	mux.HandleFunc("POST /api/v1/clusters/import", s.importCluster) // 接管已有集群（kubeconfig）
	mux.HandleFunc("POST /api/v1/clusters/probe", s.probeKubeconfig)
	mux.HandleFunc("GET /api/v1/clusters/{id}", s.getCluster)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}", s.deleteCluster)
	mux.HandleFunc("POST /api/v1/clusters/{id}/nodes", s.addClusterNode)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}/nodes/{nodeId}", s.removeClusterNode)
	mux.HandleFunc("GET /api/v1/clusters/{id}/kubeconfig", s.clusterKubeconfig)
	mux.HandleFunc("PUT /api/v1/clusters/{id}/kubeconfig", s.updateKubeconfig)

	// 集群内 Kubernetes 资源（经 kubeconfig 直连 apiserver）
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/nodes", s.clusterK8sNodes)
	mux.HandleFunc("POST /api/v1/clusters/{id}/k8s/nodes/{name}/cordon", s.k8sCordon)
	mux.HandleFunc("POST /api/v1/clusters/{id}/k8s/nodes/{name}/uncordon", s.k8sUncordon)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/metrics/nodes", s.k8sNodeMetrics) // 需 metrics-server
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/metrics/pods", s.k8sPodMetrics)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/namespaces", s.k8sNamespaces)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/pods", s.k8sPods)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}/k8s/namespaces/{ns}/pods/{name}", s.k8sDeletePod)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/namespaces/{ns}/pods/{name}/logs", s.k8sPodLogs) // 支持 ?follow=true（SSE 风格分块流）
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/namespaces/{ns}/pods/{name}/exec", s.k8sPodExec) // WebSocket
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/deployments", s.k8sDeployments)
	mux.HandleFunc("POST /api/v1/clusters/{id}/k8s/namespaces/{ns}/deployments/{name}/scale", s.k8sScale)
	mux.HandleFunc("POST /api/v1/clusters/{id}/k8s/namespaces/{ns}/deployments/{name}/restart", s.k8sRestart)
	mux.HandleFunc("POST /api/v1/clusters/{id}/k8s/namespaces/{ns}/deployments/{name}/expose", s.k8sExpose)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/services", s.k8sServices)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}/k8s/namespaces/{ns}/services/{name}", s.k8sDeleteService)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/ingresses", s.k8sIngresses)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/events", s.k8sEvents)
	mux.HandleFunc("POST /api/v1/clusters/{id}/k8s/apply", s.k8sApply)
	mux.HandleFunc("POST /api/v1/clusters/{id}/k8s/delete", s.k8sDeleteManifest)
	// 通用资源：任意 GVR（含 CRD）。core 组用 "core"（如 /resources/core/v1/configmaps）
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/resource-kinds", s.k8sResourceKinds)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/resources/{group}/{version}/{resource}", s.k8sListResources)
	mux.HandleFunc("GET /api/v1/clusters/{id}/k8s/resources/{group}/{version}/{resource}/{name}", s.k8sGetResource)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}/k8s/resources/{group}/{version}/{resource}/{name}", s.k8sDeleteResource)

	// Helm 应用：仓库按集群配置，release 直接经 kubeconfig 操作
	mux.HandleFunc("GET /api/v1/clusters/{id}/helm/repos", s.helmListRepos)
	mux.HandleFunc("POST /api/v1/clusters/{id}/helm/repos", s.helmAddRepo)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}/helm/repos/{repo}", s.helmDeleteRepo)
	mux.HandleFunc("POST /api/v1/clusters/{id}/helm/repos/{repo}/refresh", s.helmRefreshRepo)
	mux.HandleFunc("GET /api/v1/clusters/{id}/helm/charts", s.helmSearch) // ?q=&repo=
	mux.HandleFunc("GET /api/v1/clusters/{id}/helm/charts/{repo}/{chart}", s.helmChartDetail)
	mux.HandleFunc("GET /api/v1/clusters/{id}/helm/charts/{repo}/{chart}/versions", s.helmChartVersions)
	mux.HandleFunc("GET /api/v1/clusters/{id}/helm/releases", s.helmListReleases) // ?namespace=
	mux.HandleFunc("POST /api/v1/clusters/{id}/helm/releases", s.helmInstall)
	mux.HandleFunc("PUT /api/v1/clusters/{id}/helm/releases/{ns}/{name}", s.helmUpgrade)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}/helm/releases/{ns}/{name}", s.helmUninstall)
	mux.HandleFunc("POST /api/v1/clusters/{id}/helm/releases/{ns}/{name}/rollback", s.helmRollback)
	mux.HandleFunc("GET /api/v1/clusters/{id}/helm/releases/{ns}/{name}/history", s.helmHistory)
	mux.HandleFunc("GET /api/v1/clusters/{id}/helm/releases/{ns}/{name}/values", s.helmValues)

	// API 令牌（持有任一有效令牌即可管理；面板是单角色管理员模型）
	mux.HandleFunc("GET /api/v1/tokens", s.listTokens)
	mux.HandleFunc("POST /api/v1/tokens", s.createToken)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.revokeToken)

	// 审计（写操作记录）
	mux.HandleFunc("GET /api/v1/audit", s.listAudit)

	// 任务
	mux.HandleFunc("GET /api/v1/tasks", s.listTasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.getTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}/logs", s.taskLogs)
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", s.cancelTask)

	// 未匹配的 /api 路径统一 404 JSON，不落到前端页面
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, &cluster.Error{Code: "NOT_FOUND", Message: "接口不存在", Status: 404})
	})
	// Web UI（内嵌静态资源 + SPA 回退）
	mux.Handle("/", ui.Handler())
	return mux
}

// ── 节点 ─────────────────────────────────────────────────────────────────────

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if nodes == nil {
		nodes = []store.Node{}
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	n, err := s.store.GetNode(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// execNode 在节点上跑脚本，作为异步任务返回 {taskId}；日志经 /tasks/{id}/logs 轮询。
func (s *Server) execNode(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Script    string `json:"script"`
		TimeoutMs int64  `json:"timeoutMs"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Script) == "" {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "script 必填", Status: 400})
		return
	}
	if _, err := s.store.GetNode(r.Context(), nodeID); err != nil {
		writeErr(w, err)
		return
	}
	if !s.hub.Online(nodeID) {
		writeErr(w, &cluster.Error{Code: "NODE_OFFLINE", Message: "节点不在线", Status: 409})
		return
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	taskID, err := s.tasks.Start(store.TaskExec, nodeID, 0, func(ctx context.Context, logf func(string)) (agenthub.Result, error) {
		return s.hub.Exec(ctx, nodeID, req.Script, timeout, logf)
	}, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]uint64{"taskId": taskID})
}

// ── 集群 ─────────────────────────────────────────────────────────────────────

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	cs, err := s.clusters.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if cs == nil {
		cs = []store.Cluster{}
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) createCluster(w http.ResponseWriter, r *http.Request) {
	var req cluster.CreateRequest
	if !readJSON(w, r, &req) {
		return
	}
	c, err := s.clusters.Create(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// importCluster POST /api/v1/clusters/import {name, kubeconfig}：先直连探测（版本 / 节点数），通过才入库。
func (s *Server) importCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Kubeconfig string `json:"kubeconfig"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Kubeconfig) == "" {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "name 与 kubeconfig 必填", Status: 400})
		return
	}
	info, err := kube.Probe(r.Context(), req.Kubeconfig)
	if err != nil {
		writeErr(w, &cluster.Error{Code: "KUBECONFIG_UNREACHABLE", Message: err.Error(), Status: 400})
		return
	}
	c, err := s.clusters.Import(r.Context(), cluster.ImportRequest{
		Name: req.Name, Kubeconfig: req.Kubeconfig,
		K8sVersion: info.Version, Endpoint: info.Endpoint, PodCIDR: info.PodCIDR,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"cluster": c, "probe": info})
}

// probeKubeconfig POST /api/v1/clusters/probe {kubeconfig}：只探测不入库（导入表单预检）。
func (s *Server) probeKubeconfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	info, err := kube.Probe(r.Context(), req.Kubeconfig)
	if err != nil {
		writeErr(w, &cluster.Error{Code: "KUBECONFIG_UNREACHABLE", Message: err.Error(), Status: 400})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// updateKubeconfig PUT /api/v1/clusters/{id}/kubeconfig {kubeconfig}：接管集群证书轮换后替换。
func (s *Server) updateKubeconfig(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if _, err := kube.Probe(r.Context(), req.Kubeconfig); err != nil {
		writeErr(w, &cluster.Error{Code: "KUBECONFIG_UNREACHABLE", Message: err.Error(), Status: 400})
		return
	}
	if err := s.clusters.UpdateKubeconfig(r.Context(), id, req.Kubeconfig); err != nil {
		writeErr(w, err)
		return
	}
	s.kube.Forget(id)
	w.WriteHeader(http.StatusNoContent)
}

type clusterDetail struct {
	store.Cluster
	Members []store.Member `json:"members"`
}

func (s *Server) getCluster(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	c, err := s.clusters.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	ms, err := s.clusters.Members(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ms == nil {
		ms = []store.Member{}
	}
	writeJSON(w, http.StatusOK, clusterDetail{Cluster: c, Members: ms})
}

func (s *Server) addClusterNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		NodeID uint64 `json:"nodeId"`
		Role   string `json:"role"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	taskID, err := s.clusters.AddNode(r.Context(), id, req.NodeID, req.Role)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]uint64{"taskId": taskID})
}

// removeClusterNode 移除成员，异步任务返回 {taskId}。
func (s *Server) removeClusterNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	nodeID, ok := pathID(w, r, "nodeId")
	if !ok {
		return
	}
	taskID, err := s.clusters.RemoveNode(r.Context(), id, nodeID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]uint64{"taskId": taskID})
}

// deleteCluster 删集群；?force=true 时对在线成员逐个发 reset 任务，返回 {taskIds}。
func (s *Server) deleteCluster(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	taskIDs, err := s.clusters.Delete(r.Context(), id, force)
	if err != nil {
		writeErr(w, err)
		return
	}
	if taskIDs == nil {
		taskIDs = []uint64{}
	}
	writeJSON(w, http.StatusOK, map[string][]uint64{"taskIds": taskIDs})
}

func (s *Server) clusterKubeconfig(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	kc, err := s.clusters.Kubeconfig(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = io.WriteString(w, kc)
}

// ── API 令牌 ─────────────────────────────────────────────────────────────────

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.store.ListTokens(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if ts == nil {
		ts = []store.APIToken{}
	}
	writeJSON(w, http.StatusOK, ts)
}

// createToken 明文只在这次响应里出现。
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "name 必填", Status: 400})
		return
	}
	t, plain, err := s.store.CreateToken(r.Context(), req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "plain": plain})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.store.RevokeToken(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── 任务 ─────────────────────────────────────────────────────────────────────

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nodeID, _ := strconv.ParseUint(q.Get("nodeId"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	ts, err := s.store.ListTasks(r.Context(), nodeID, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ts == nil {
		ts = []store.Task{}
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	t, err := s.store.GetTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// taskLogs 增量拉日志：?after=<seq>，返回 {task, logs}，调用方用最后一条 seq 续拉，task.status 非 running 即结束。
func (s *Server) taskLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	t, err := s.store.GetTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logs, err := s.store.TaskLogs(r.Context(), id, after, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t, "logs": logs})
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.tasks.Cancel(id); err != nil {
		if errors.Is(err, task.ErrNotRunning) {
			writeErr(w, &cluster.Error{Code: "TASK_NOT_RUNNING", Message: "任务不在运行中", Status: 409})
			return
		}
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── 工具 ─────────────────────────────────────────────────────────────────────

func pathID(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue(name), 10, 64)
	if err != nil || id == 0 {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "非法 " + name, Status: 400})
		return 0, false
	}
	return id, true
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "请求体不是合法 JSON: " + err.Error(), Status: 400})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 统一错误映射：store.ErrNotFound → 404；cluster.Error 按其 Status；其余 500。
func writeErr(w http.ResponseWriter, err error) {
	var ce *cluster.Error
	switch {
	case errors.As(err, &ce):
		writeJSON(w, ce.Status, map[string]any{"error": map[string]string{"code": ce.Code, "message": ce.Message}})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "资源不存在"}})
	default:
		log.Printf("[panel] 内部错误: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "INTERNAL", "message": err.Error()}})
	}
}
