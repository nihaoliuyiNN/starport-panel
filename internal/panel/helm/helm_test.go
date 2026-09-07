package helm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
)

// 起一个静态 Helm 仓库：index.yaml + 两个版本的 demo chart，验证索引缓存 / 检索 / 版本 / 下载解析。
func newRepoServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	for _, v := range []string{"1.0.0", "1.1.0"} {
		ch := &chart.Chart{
			Metadata: &chart.Metadata{APIVersion: "v2", Name: "demo", Version: v, AppVersion: "app-" + v,
				Description: "Demo web server", Keywords: []string{"web", "nginx"}},
			// chartutil.Save 从 Raw 取 values.yaml（保留注释），README 等其它文件取 Files
			Raw:       []*chart.File{{Name: "values.yaml", Data: []byte("replicaCount: 1\nimage: nginx\n")}},
			Files:     []*chart.File{{Name: "README.md", Data: []byte("# demo\n")}},
			Values:    map[string]interface{}{"replicaCount": 1, "image": "nginx"},
			Templates: []*chart.File{{Name: "templates/cm.yaml", Data: []byte("kind: ConfigMap\napiVersion: v1\nmetadata:\n  name: x\n")}},
		}
		if _, err := chartutil.Save(ch, dir); err != nil {
			t.Fatal(err)
		}
	}
	// 另一个不带下载地址的 chart，只在索引里
	index := `apiVersion: v1
entries:
  demo:
    - name: demo
      version: 1.1.0
      appVersion: app-1.1.0
      description: Demo web server
      keywords: [web, nginx]
      urls: [demo-1.1.0.tgz]
    - name: demo
      version: 1.0.0
      appVersion: app-1.0.0
      description: Demo web server
      urls: [demo-1.0.0.tgz]
  other:
    - name: other
      version: 0.1.0
      description: Something else entirely
      urls: [https://example.invalid/other-0.1.0.tgz]
`
	if err := os.WriteFile(filepath.Join(dir, "index.yaml"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.yaml" {
			hits++
			w.Header().Set("X-Hits", "")
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)
	return srv, dir
}

func TestIndexSearchAndChart(t *testing.T) {
	srv, _ := newRepoServer(t)
	ctx := context.Background()
	c := New()
	r := Repo{Name: "test", URL: srv.URL, Username: "u", Password: "p"}

	// 检索：关键字命中名称 / 描述 / keywords
	charts, errs := c.Search(ctx, []Repo{r}, "")
	if len(errs) != 0 || len(charts) != 2 {
		t.Fatalf("search all: %+v errs=%v", charts, errs)
	}
	if charts[0].Name != "demo" || charts[0].Version != "1.1.0" || charts[0].Versions != 2 || charts[0].AppVersion != "app-1.1.0" {
		t.Errorf("latest demo: %+v", charts[0])
	}
	if got, _ := c.Search(ctx, []Repo{r}, "NGINX"); len(got) != 1 || got[0].Name != "demo" {
		t.Errorf("keyword search: %+v", got)
	}
	if got, _ := c.Search(ctx, []Repo{r}, "else"); len(got) != 1 || got[0].Name != "other" {
		t.Errorf("description search: %+v", got)
	}

	// 版本列表新→旧
	vers, err := c.Versions(ctx, r, "demo")
	if err != nil || len(vers) != 2 || vers[0].Version != "1.1.0" {
		t.Fatalf("versions: %+v %v", vers, err)
	}
	if _, err := c.Versions(ctx, r, "nope"); err != ErrChartNotFound {
		t.Errorf("missing chart: %v", err)
	}

	// 详情：下载 tgz 解析出 values / README；空版本取最新，指定版本取指定
	d, err := c.Detail(ctx, r, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "1.1.0" || d.Values != "replicaCount: 1\nimage: nginx\n" || d.Readme != "# demo\n" {
		t.Errorf("detail: %+v", d)
	}
	if d2, err := c.Detail(ctx, r, "demo", "1.0.0"); err != nil || d2.Version != "1.0.0" {
		t.Errorf("detail 1.0.0: %+v %v", d2, err)
	}
	if _, err := c.Detail(ctx, r, "demo", "9.9.9"); err == nil {
		t.Error("want error for missing version")
	}

	// 鉴权错误与不可达仓库：Search 汇总不中断
	bad := Repo{Name: "bad", URL: srv.URL, Username: "x", Password: "y"}
	oci := Repo{Name: "oci", URL: "oci://registry.example/charts"}
	charts, errs = c.Search(ctx, []Repo{r, bad, oci}, "demo")
	if len(charts) != 1 || errs["bad"] == "" || errs["oci"] == "" || errs["test"] != "" {
		t.Errorf("mixed repos: charts=%d errs=%v", len(charts), errs)
	}

	// values 解析
	if _, err := parseValues("a: [b"); err == nil {
		t.Error("bad yaml must fail")
	}
	if v, err := parseValues("  \n"); err != nil || len(v) != 0 {
		t.Errorf("empty values: %v %v", v, err)
	}
}

func TestRESTGetterFromKubeconfig(t *testing.T) {
	kc := `apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: https://10.0.0.1:6443, insecure-skip-tls-verify: true}}]
users: [{name: u, user: {token: abc}}]
contexts: [{name: x, context: {cluster: c, user: u, namespace: team-a}}]
current-context: x
`
	g, err := newGetter(kc, "apps")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := g.ToRESTConfig()
	if cfg.Host != "https://10.0.0.1:6443" || cfg.BearerToken != "abc" {
		t.Errorf("rest config: %+v", cfg)
	}
	if ns, _, _ := g.ToRawKubeConfigLoader().Namespace(); ns != "apps" {
		t.Errorf("namespace override: %q", ns)
	}
	g2, _ := newGetter(kc, "")
	if ns, _, _ := g2.ToRawKubeConfigLoader().Namespace(); ns != "team-a" {
		t.Errorf("kubeconfig namespace: %q", ns)
	}
	if _, err := newGetter("not: [valid", ""); err == nil {
		t.Error("bad kubeconfig must fail")
	}
}
