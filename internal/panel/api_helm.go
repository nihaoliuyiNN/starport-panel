package panel

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"starport-panel/internal/panel/cluster"
	"starport-panel/internal/panel/helm"
	"starport-panel/internal/panel/store"
)

// Helm 应用管理 HTTP 处理器。仓库按集群配置；release 操作直接用集群 kubeconfig 走 Helm SDK（同步执行）。

var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func helmErr(err error) error {
	switch {
	case errors.Is(err, helm.ErrChartNotFound):
		return &cluster.Error{Code: "CHART_NOT_FOUND", Message: err.Error(), Status: 404}
	case errors.Is(err, helm.ErrReleaseNotFound):
		return &cluster.Error{Code: "RELEASE_NOT_FOUND", Message: err.Error(), Status: 404}
	case errors.Is(err, helm.ErrOCIUnsupported):
		return &cluster.Error{Code: "OCI_UNSUPPORTED", Message: err.Error(), Status: 400}
	case errors.Is(err, store.ErrNotFound):
		return err
	}
	return &cluster.Error{Code: "HELM_ERROR", Message: err.Error(), Status: 502}
}

func toHelmRepo(r store.HelmRepo) helm.Repo {
	return helm.Repo{Name: r.Name, URL: r.URL, Username: r.Username, Password: r.Password}
}

// repoFor 取路径 {repo} 对应的仓库记录；失败已写响应。
func (s *Server) repoFor(w http.ResponseWriter, r *http.Request, clusterID uint64, name string) (helm.Repo, bool) {
	rec, err := s.store.GetHelmRepo(r.Context(), clusterID, name)
	if err != nil {
		writeErr(w, err)
		return helm.Repo{}, false
	}
	return toHelmRepo(rec), true
}

// ── 仓库 ──────────────────────────────────────────────────────────────────────

func (s *Server) helmListRepos(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.clusters.Get(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	repos, err := s.store.ListHelmRepos(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if repos == nil {
		repos = []store.HelmRepo{}
	}
	writeJSON(w, http.StatusOK, repos)
}

// helmAddRepo POST .../helm/repos {name,url,username?,password?}：先拉一次索引验证可达再入库。
func (s *Server) helmAddRepo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.clusters.Get(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	if !dns1123.MatchString(req.Name) {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "仓库名只能是小写字母、数字、'-'", Status: 400})
		return
	}
	if u, err := url.Parse(req.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		if strings.HasPrefix(req.URL, "oci://") {
			writeErr(w, helmErr(helm.ErrOCIUnsupported))
			return
		}
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "url 须为 http(s) 地址", Status: 400})
		return
	}
	rec := store.HelmRepo{ClusterID: id, Name: req.Name, URL: req.URL, Username: req.Username, Password: req.Password}
	if err := s.helm.Refresh(r.Context(), toHelmRepo(rec)); err != nil {
		writeErr(w, &cluster.Error{Code: "REPO_UNREACHABLE", Message: err.Error(), Status: 400})
		return
	}
	if err := s.store.AddHelmRepo(r.Context(), &rec); err != nil {
		if store.IsConflict(err) {
			writeErr(w, &cluster.Error{Code: "REPO_NAME_EXISTS", Message: "仓库名已存在", Status: 409})
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) helmDeleteRepo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	name := r.PathValue("repo")
	rec, err := s.store.GetHelmRepo(r.Context(), id, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.store.DeleteHelmRepo(r.Context(), id, name); err != nil {
		writeErr(w, err)
		return
	}
	s.helm.Forget(toHelmRepo(rec))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) helmRefreshRepo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	repo, ok := s.repoFor(w, r, id, r.PathValue("repo"))
	if !ok {
		return
	}
	if err := s.helm.Refresh(r.Context(), repo); err != nil {
		writeErr(w, &cluster.Error{Code: "REPO_UNREACHABLE", Message: err.Error(), Status: 502})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Chart ─────────────────────────────────────────────────────────────────────

// helmSearch GET .../helm/charts?q=&repo= → {charts, errors:{repo:msg}}
func (s *Server) helmSearch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	recs, err := s.store.ListHelmRepos(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	only := r.URL.Query().Get("repo")
	var repos []helm.Repo
	for _, rec := range recs {
		if only == "" || rec.Name == only {
			repos = append(repos, toHelmRepo(rec))
		}
	}
	charts, errs := s.helm.Search(r.Context(), repos, r.URL.Query().Get("q"))
	if charts == nil {
		charts = []helm.Chart{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"charts": charts, "errors": errs})
}

func (s *Server) helmChartVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	repo, ok := s.repoFor(w, r, id, r.PathValue("repo"))
	if !ok {
		return
	}
	vers, err := s.helm.Versions(r.Context(), repo, r.PathValue("chart"))
	if err != nil {
		writeErr(w, helmErr(err))
		return
	}
	writeJSON(w, http.StatusOK, vers)
}

// helmChartDetail GET .../helm/charts/{repo}/{chart}?version= → 默认 values + README
func (s *Server) helmChartDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	repo, ok := s.repoFor(w, r, id, r.PathValue("repo"))
	if !ok {
		return
	}
	d, err := s.helm.Detail(r.Context(), repo, r.PathValue("chart"), r.URL.Query().Get("version"))
	if err != nil {
		writeErr(w, helmErr(err))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ── Release ───────────────────────────────────────────────────────────────────

type releaseRequest struct {
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	Repo            string `json:"repo"`
	Chart           string `json:"chart"`
	Version         string `json:"version"`
	Values          string `json:"values"`
	CreateNamespace bool   `json:"createNamespace"`
	Wait            bool   `json:"wait"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
}

func (q *releaseRequest) toInstall() (helm.InstallRequest, error) {
	q.Namespace = strings.TrimSpace(q.Namespace)
	q.Name = strings.TrimSpace(q.Name)
	if q.Namespace == "" {
		q.Namespace = "default"
	}
	if !dns1123.MatchString(q.Name) || len(q.Name) > 53 {
		return helm.InstallRequest{}, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "release 名须为 ≤53 位小写字母 / 数字 / '-'", Status: 400}
	}
	if strings.TrimSpace(q.Chart) == "" || strings.TrimSpace(q.Repo) == "" {
		return helm.InstallRequest{}, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "repo 与 chart 必填", Status: 400}
	}
	return helm.InstallRequest{
		Namespace: q.Namespace, ReleaseName: q.Name, Repo: q.Repo, Chart: strings.TrimSpace(q.Chart), Version: strings.TrimSpace(q.Version),
		ValuesYAML: q.Values, CreateNamespace: q.CreateNamespace, Wait: q.Wait, Timeout: time.Duration(q.TimeoutSeconds) * time.Second,
	}, nil
}

func (s *Server) helmListReleases(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	_ = id
	rels, err := s.helm.List(r.Context(), kc, r.URL.Query().Get("namespace"))
	if err != nil {
		writeErr(w, helmErr(err))
		return
	}
	writeJSON(w, http.StatusOK, rels)
}

// helmInstall POST .../helm/releases
func (s *Server) helmInstall(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	var q releaseRequest
	if !readJSON(w, r, &q) {
		return
	}
	req, err := q.toInstall()
	if err != nil {
		writeErr(w, err)
		return
	}
	repo, ok := s.repoFor(w, r, id, req.Repo)
	if !ok {
		return
	}
	rel, err := s.helm.Install(r.Context(), kc, repo, req)
	if err != nil {
		writeErr(w, helmErr(err))
		return
	}
	writeJSON(w, http.StatusCreated, rel)
}

// helmUpgrade PUT .../helm/releases/{ns}/{name}
func (s *Server) helmUpgrade(w http.ResponseWriter, r *http.Request) {
	id, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	var q releaseRequest
	if !readJSON(w, r, &q) {
		return
	}
	q.Namespace, q.Name = r.PathValue("ns"), r.PathValue("name")
	req, err := q.toInstall()
	if err != nil {
		writeErr(w, err)
		return
	}
	repo, ok := s.repoFor(w, r, id, req.Repo)
	if !ok {
		return
	}
	rel, err := s.helm.Upgrade(r.Context(), kc, repo, req)
	if err != nil {
		writeErr(w, helmErr(err))
		return
	}
	writeJSON(w, http.StatusOK, rel)
}

func (s *Server) helmUninstall(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	if err := s.helm.Uninstall(r.Context(), kc, r.PathValue("ns"), r.PathValue("name")); err != nil {
		writeErr(w, helmErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// helmRollback POST .../helm/releases/{ns}/{name}/rollback {revision}（0 = 上一版）
func (s *Server) helmRollback(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	var req struct {
		Revision int `json:"revision"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Revision < 0 {
		writeErr(w, &cluster.Error{Code: "INVALID_ARGUMENT", Message: "revision 不能为负", Status: 400})
		return
	}
	if err := s.helm.Rollback(r.Context(), kc, r.PathValue("ns"), r.PathValue("name"), req.Revision); err != nil {
		writeErr(w, helmErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) helmHistory(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	hs, err := s.helm.History(r.Context(), kc, r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		writeErr(w, helmErr(err))
		return
	}
	writeJSON(w, http.StatusOK, hs)
}

// helmValues GET .../helm/releases/{ns}/{name}/values → {values: "<yaml>"}
func (s *Server) helmValues(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := s.kubeconfigFor(w, r)
	if !ok {
		return
	}
	v, err := s.helm.Values(r.Context(), kc, r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		writeErr(w, helmErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"values": v})
}
