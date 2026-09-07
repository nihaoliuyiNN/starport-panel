// Package helm 用 Helm Go SDK 在集群里管理应用：仓库索引、Chart 检索、Release 安装 / 升级 / 回滚 / 卸载。
// 不落 helm 的用户目录（~/.config/helm 等），仓库配置存在面板数据库；索引在内存按 TTL 缓存。
// 目前只支持 HTTP(S) 仓库，OCI 仓库（oci://）暂不支持。
package helm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

// 包级错误。
var (
	ErrChartNotFound   = errors.New("helm: chart 不存在")
	ErrReleaseNotFound = errors.New("helm: release 不存在")
	ErrOCIUnsupported  = errors.New("helm: 暂不支持 oci:// 仓库")
)

// Repo 一个 Chart 仓库（持久化在 store，这里只要连接信息）。
type Repo struct {
	Name     string
	URL      string
	Username string
	Password string
}

// Chart 检索结果：每个 chart 取最新版本。
type Chart struct {
	Repo        string   `json:"repo"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	AppVersion  string   `json:"appVersion"`
	Description string   `json:"description"`
	Icon        string   `json:"icon,omitempty"`
	Home        string   `json:"home,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Versions    int      `json:"versions"` // 可选版本数
}

// ChartVersion 某 chart 的一个版本。
type ChartVersion struct {
	Version    string    `json:"version"`
	AppVersion string    `json:"appVersion"`
	Created    time.Time `json:"created"`
}

// ChartDetail 安装表单需要的资料：默认 values 与 README。
type ChartDetail struct {
	Chart
	Readme string `json:"readme"`
	Values string `json:"values"` // 默认 values.yaml 原文
}

// Release 已部署的应用。
type Release struct {
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace"`
	Revision    int       `json:"revision"`
	Status      string    `json:"status"`
	Chart       string    `json:"chart"` // name-version
	ChartName   string    `json:"chartName"`
	ChartVer    string    `json:"chartVersion"`
	AppVersion  string    `json:"appVersion"`
	Description string    `json:"description,omitempty"`
	Updated     time.Time `json:"updated"`
	Notes       string    `json:"notes,omitempty"`
}

// InstallRequest 安装 / 升级参数。
type InstallRequest struct {
	Namespace       string
	ReleaseName     string
	Repo            string // 仓库名
	Chart           string // chart 名
	Version         string // 空 = 最新
	ValuesYAML      string // 用户 values（覆盖默认）
	CreateNamespace bool
	Wait            bool
	Timeout         time.Duration
}

// Client 见包注释。
type Client struct {
	mu      sync.Mutex
	indexes map[string]cachedIndex // key: repo URL
	ttl     time.Duration
}

type cachedIndex struct {
	idx     *repo.IndexFile
	fetched time.Time
}

// New 建客户端；索引缓存 10 分钟。
func New() *Client { return &Client{indexes: map[string]cachedIndex{}, ttl: 10 * time.Minute} }

// ── 仓库索引 ──────────────────────────────────────────────────────────────────

// Refresh 强制重新拉取某仓库索引。
func (c *Client) Refresh(ctx context.Context, r Repo) error {
	_, err := c.fetchIndex(ctx, r, true)
	return err
}

func (c *Client) index(ctx context.Context, r Repo) (*repo.IndexFile, error) {
	return c.fetchIndex(ctx, r, false)
}

func (c *Client) fetchIndex(ctx context.Context, r Repo, force bool) (*repo.IndexFile, error) {
	if strings.HasPrefix(r.URL, "oci://") {
		return nil, ErrOCIUnsupported
	}
	key := cacheKey(r)
	c.mu.Lock()
	if e, ok := c.indexes[key]; ok && !force && time.Since(e.fetched) < c.ttl {
		c.mu.Unlock()
		return e.idx, nil
	}
	c.mu.Unlock()

	g, err := getter.NewHTTPGetter(getter.WithTimeout(60 * time.Second))
	if err != nil {
		return nil, err
	}
	indexURL := strings.TrimSuffix(r.URL, "/") + "/index.yaml"
	buf, err := g.Get(indexURL, getter.WithURL(r.URL), getter.WithBasicAuth(r.Username, r.Password), getter.WithTimeout(60*time.Second))
	if err != nil {
		return nil, fmt.Errorf("helm: 拉取索引 %s: %w", indexURL, err)
	}
	idx, err := parseIndex(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("helm: 解析索引 %s: %w", indexURL, err)
	}
	c.mu.Lock()
	c.indexes[key] = cachedIndex{idx: idx, fetched: time.Now()}
	c.mu.Unlock()
	log.Printf("[helm] 已刷新仓库索引 %s（%d 个 chart）", r.Name, len(idx.Entries))
	return idx, nil
}

// parseIndex 复用 helm 的 LoadIndexFile（会剔除无效条目并排序）；它只接受路径，故经临时文件。
func parseIndex(data []byte) (*repo.IndexFile, error) {
	f, err := os.CreateTemp("", "starport-helm-index-*.yaml")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, err
	}
	_ = f.Close()
	return repo.LoadIndexFile(name)
}

// Forget 丢掉某仓库缓存（删仓库时）。
func (c *Client) Forget(r Repo) {
	c.mu.Lock()
	delete(c.indexes, cacheKey(r))
	c.mu.Unlock()
}

// cacheKey 同一 URL 不同凭据分开缓存（凭据错的那份不能拿到别人的索引）。
func cacheKey(r Repo) string { return r.URL + "\x00" + r.Username }

// ── Chart 检索 ────────────────────────────────────────────────────────────────

// Search 在给定仓库里按关键字（名称 / 描述 / 关键词，忽略大小写）检索；keyword 空返回全部。
// 单个仓库拉取失败不影响其它仓库，错误汇总在返回值 errs 里（key 仓库名）。
func (c *Client) Search(ctx context.Context, repos []Repo, keyword string) (charts []Chart, errs map[string]string) {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	errs = map[string]string{}
	for _, r := range repos {
		idx, err := c.index(ctx, r)
		if err != nil {
			errs[r.Name] = err.Error()
			continue
		}
		for name, vers := range idx.Entries {
			if len(vers) == 0 || vers[0] == nil || vers[0].Metadata == nil {
				continue
			}
			latest := vers[0]
			if kw != "" && !matches(latest, kw) {
				continue
			}
			charts = append(charts, Chart{
				Repo: r.Name, Name: name, Version: latest.Version, AppVersion: latest.AppVersion,
				Description: latest.Description, Icon: latest.Icon, Home: latest.Home, Keywords: latest.Keywords,
				Deprecated: latest.Deprecated, Versions: len(vers),
			})
		}
	}
	sort.Slice(charts, func(i, j int) bool {
		if charts[i].Name != charts[j].Name {
			return charts[i].Name < charts[j].Name
		}
		return charts[i].Repo < charts[j].Repo
	})
	return charts, errs
}

func matches(cv *repo.ChartVersion, kw string) bool {
	if strings.Contains(strings.ToLower(cv.Name), kw) || strings.Contains(strings.ToLower(cv.Description), kw) {
		return true
	}
	for _, k := range cv.Keywords {
		if strings.Contains(strings.ToLower(k), kw) {
			return true
		}
	}
	return false
}

// Versions 某 chart 的全部版本（新→旧）。
func (c *Client) Versions(ctx context.Context, r Repo, name string) ([]ChartVersion, error) {
	idx, err := c.index(ctx, r)
	if err != nil {
		return nil, err
	}
	vers, ok := idx.Entries[name]
	if !ok {
		return nil, ErrChartNotFound
	}
	out := make([]ChartVersion, 0, len(vers))
	for _, v := range vers {
		if v == nil || v.Metadata == nil {
			continue
		}
		out = append(out, ChartVersion{Version: v.Version, AppVersion: v.AppVersion, Created: v.Created})
	}
	return out, nil
}

// Detail 下载 chart 包，取 README 与默认 values。
func (c *Client) Detail(ctx context.Context, r Repo, name, version string) (ChartDetail, error) {
	ch, cv, err := c.loadChart(ctx, r, name, version)
	if err != nil {
		return ChartDetail{}, err
	}
	d := ChartDetail{Chart: Chart{
		Repo: r.Name, Name: ch.Name(), Version: ch.Metadata.Version, AppVersion: ch.Metadata.AppVersion,
		Description: ch.Metadata.Description, Icon: ch.Metadata.Icon, Home: ch.Metadata.Home, Keywords: ch.Metadata.Keywords,
		Deprecated: ch.Metadata.Deprecated,
	}}
	_ = cv
	// Raw 是包内全部文件；README 等非模板文件另在 Files 里，两处都看
	for _, f := range append(append([]*chart.File{}, ch.Raw...), ch.Files...) {
		switch strings.ToLower(f.Name) {
		case "readme.md":
			if d.Readme == "" {
				d.Readme = string(f.Data)
			}
		case "values.yaml":
			if d.Values == "" {
				d.Values = string(f.Data)
			}
		}
	}
	return d, nil
}

// loadChart 从索引定位 chart 包 URL 并下载解析。
func (c *Client) loadChart(ctx context.Context, r Repo, name, version string) (*chart.Chart, *repo.ChartVersion, error) {
	idx, err := c.index(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	cv, err := idx.Get(name, version)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s %s", ErrChartNotFound, name, version)
	}
	if len(cv.URLs) == 0 {
		return nil, nil, fmt.Errorf("helm: chart %s 索引没有下载地址", name)
	}
	u, err := repo.ResolveReferenceURL(r.URL, cv.URLs[0])
	if err != nil {
		return nil, nil, fmt.Errorf("helm: 解析 chart 地址: %w", err)
	}
	g, err := getter.NewHTTPGetter(getter.WithTimeout(120 * time.Second))
	if err != nil {
		return nil, nil, err
	}
	buf, err := g.Get(u, getter.WithURL(r.URL), getter.WithBasicAuth(r.Username, r.Password), getter.WithTimeout(120*time.Second))
	if err != nil {
		return nil, nil, fmt.Errorf("helm: 下载 chart %s: %w", u, err)
	}
	ch, err := loader.LoadArchive(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, nil, fmt.Errorf("helm: 解析 chart 包: %w", err)
	}
	return ch, cv, nil
}

// ── Release ───────────────────────────────────────────────────────────────────

// Install 安装。
func (c *Client) Install(ctx context.Context, kubeconfig string, r Repo, req InstallRequest) (Release, error) {
	ch, _, err := c.loadChart(ctx, r, req.Chart, req.Version)
	if err != nil {
		return Release{}, err
	}
	vals, err := parseValues(req.ValuesYAML)
	if err != nil {
		return Release{}, err
	}
	cfg, err := actionConfig(kubeconfig, req.Namespace)
	if err != nil {
		return Release{}, err
	}
	inst := action.NewInstall(cfg)
	inst.ReleaseName = req.ReleaseName
	inst.Namespace = req.Namespace
	inst.CreateNamespace = req.CreateNamespace
	inst.Wait = req.Wait
	inst.Timeout = timeoutOr(req.Timeout)
	inst.Description = "starport-panel"
	rel, err := inst.RunWithContext(ctx, ch, vals)
	if err != nil {
		return Release{}, fmt.Errorf("helm: 安装失败: %w", err)
	}
	return toRelease(rel), nil
}

// Upgrade 升级（可换版本 / 改 values）。values 以本次为准（不复用旧值），避免"改了没生效"的困惑。
func (c *Client) Upgrade(ctx context.Context, kubeconfig string, r Repo, req InstallRequest) (Release, error) {
	ch, _, err := c.loadChart(ctx, r, req.Chart, req.Version)
	if err != nil {
		return Release{}, err
	}
	vals, err := parseValues(req.ValuesYAML)
	if err != nil {
		return Release{}, err
	}
	cfg, err := actionConfig(kubeconfig, req.Namespace)
	if err != nil {
		return Release{}, err
	}
	up := action.NewUpgrade(cfg)
	up.Namespace = req.Namespace
	up.Wait = req.Wait
	up.Timeout = timeoutOr(req.Timeout)
	up.MaxHistory = 10
	up.Description = "starport-panel"
	rel, err := up.RunWithContext(ctx, req.ReleaseName, ch, vals)
	if err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return Release{}, ErrReleaseNotFound
		}
		return Release{}, fmt.Errorf("helm: 升级失败: %w", err)
	}
	return toRelease(rel), nil
}

// Uninstall 卸载。
func (c *Client) Uninstall(_ context.Context, kubeconfig, namespace, name string) error {
	cfg, err := actionConfig(kubeconfig, namespace)
	if err != nil {
		return err
	}
	un := action.NewUninstall(cfg)
	un.Timeout = 5 * time.Minute
	if _, err := un.Run(name); err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return ErrReleaseNotFound
		}
		return fmt.Errorf("helm: 卸载失败: %w", err)
	}
	return nil
}

// Rollback 回滚到指定 revision（0 = 上一版）。
func (c *Client) Rollback(_ context.Context, kubeconfig, namespace, name string, revision int) error {
	cfg, err := actionConfig(kubeconfig, namespace)
	if err != nil {
		return err
	}
	rb := action.NewRollback(cfg)
	rb.Version = revision
	rb.Timeout = 5 * time.Minute
	rb.MaxHistory = 10
	if err := rb.Run(name); err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return ErrReleaseNotFound
		}
		return fmt.Errorf("helm: 回滚失败: %w", err)
	}
	return nil
}

// List 列 release；namespace 空 = 全部命名空间。含 failed / pending 等所有状态。
func (c *Client) List(_ context.Context, kubeconfig, namespace string) ([]Release, error) {
	cfg, err := actionConfig(kubeconfig, namespace)
	if err != nil {
		return nil, err
	}
	l := action.NewList(cfg)
	l.AllNamespaces = namespace == ""
	l.All = true
	l.SetStateMask()
	rels, err := l.Run()
	if err != nil {
		return nil, fmt.Errorf("helm: 列 release: %w", err)
	}
	out := make([]Release, 0, len(rels))
	for _, r := range rels {
		out = append(out, toRelease(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// History 某 release 的修订历史（新→旧）。
func (c *Client) History(_ context.Context, kubeconfig, namespace, name string) ([]Release, error) {
	cfg, err := actionConfig(kubeconfig, namespace)
	if err != nil {
		return nil, err
	}
	rels, err := action.NewHistory(cfg).Run(name)
	if err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return nil, ErrReleaseNotFound
		}
		return nil, fmt.Errorf("helm: 读历史: %w", err)
	}
	out := make([]Release, 0, len(rels))
	for _, r := range rels {
		out = append(out, toRelease(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	return out, nil
}

// Values 某 release 用户提供的 values（YAML 文本），升级表单回填用。
func (c *Client) Values(_ context.Context, kubeconfig, namespace, name string) (string, error) {
	cfg, err := actionConfig(kubeconfig, namespace)
	if err != nil {
		return "", err
	}
	vals, err := action.NewGetValues(cfg).Run(name)
	if err != nil {
		if errors.Is(err, driver.ErrReleaseNotFound) {
			return "", ErrReleaseNotFound
		}
		return "", fmt.Errorf("helm: 读 values: %w", err)
	}
	if len(vals) == 0 {
		return "", nil
	}
	b, err := yaml.Marshal(vals)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ── 内部 ──────────────────────────────────────────────────────────────────────

func parseValues(s string) (map[string]interface{}, error) {
	vals := map[string]interface{}{}
	if strings.TrimSpace(s) == "" {
		return vals, nil
	}
	if err := yaml.Unmarshal([]byte(s), &vals); err != nil {
		return nil, fmt.Errorf("helm: values 不是合法 YAML: %w", err)
	}
	return vals, nil
}

func timeoutOr(d time.Duration) time.Duration {
	if d <= 0 {
		return 5 * time.Minute
	}
	return d
}

func toRelease(r *release.Release) Release {
	out := Release{Name: r.Name, Namespace: r.Namespace, Revision: r.Version}
	if r.Info != nil {
		out.Status = r.Info.Status.String()
		out.Description = r.Info.Description
		out.Updated = r.Info.LastDeployed.Time
		out.Notes = r.Info.Notes
	}
	if r.Chart != nil && r.Chart.Metadata != nil {
		out.ChartName = r.Chart.Metadata.Name
		out.ChartVer = r.Chart.Metadata.Version
		out.AppVersion = r.Chart.Metadata.AppVersion
		out.Chart = out.ChartName + "-" + out.ChartVer
	}
	return out
}

// actionConfig 每次调用新建（Helm 的 Configuration 绑定命名空间，且内部不是并发安全的）。
func actionConfig(kubeconfig, namespace string) (*action.Configuration, error) {
	g, err := newGetter(kubeconfig, namespace)
	if err != nil {
		return nil, err
	}
	cfg := new(action.Configuration)
	if err := cfg.Init(g, namespace, "secret", func(format string, v ...interface{}) {
		log.Printf("[helm] "+format, v...)
	}); err != nil {
		return nil, fmt.Errorf("helm: 初始化: %w", err)
	}
	return cfg, nil
}

// restGetter 实现 genericclioptions.RESTClientGetter：从 kubeconfig 文本出发，不读磁盘。
type restGetter struct {
	raw       clientcmd.ClientConfig
	cfg       *rest.Config
	namespace string
}

func newGetter(kubeconfig, namespace string) (*restGetter, error) {
	raw, err := clientcmd.NewClientConfigFromBytes([]byte(kubeconfig))
	if err != nil {
		return nil, fmt.Errorf("helm: 解析 kubeconfig: %w", err)
	}
	cfg, err := raw.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("helm: kubeconfig 转 rest 配置: %w", err)
	}
	cfg.UserAgent = "starport-panel/helm"
	cfg.QPS, cfg.Burst = 50, 100
	return &restGetter{raw: raw, cfg: cfg, namespace: namespace}, nil
}

func (g *restGetter) ToRESTConfig() (*rest.Config, error) { return rest.CopyConfig(g.cfg), nil }

func (g *restGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(dc), nil
}

func (g *restGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(dc), nil
}

func (g *restGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	if g.namespace == "" {
		return g.raw
	}
	// 覆盖默认命名空间，helm 在 Init 里会读它
	return &nsOverride{inner: g.raw, ns: g.namespace}
}

// nsOverride 包一层 clientcmd.ClientConfig，只改 Namespace()（接口里有同名方法 ClientConfig()，不能匿名内嵌）。
type nsOverride struct {
	inner clientcmd.ClientConfig
	ns    string
}

func (n *nsOverride) RawConfig() (clientcmdapi.Config, error) { return n.inner.RawConfig() }
func (n *nsOverride) ClientConfig() (*rest.Config, error)     { return n.inner.ClientConfig() }
func (n *nsOverride) Namespace() (string, bool, error)        { return n.ns, true, nil }
func (n *nsOverride) ConfigAccess() clientcmd.ConfigAccess    { return n.inner.ConfigAccess() }
