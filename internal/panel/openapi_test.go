package panel

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestOpenAPICoversRoutes 防止接口与文档漂移：api.go 里注册的每条 /api/v1 路由都必须在 openapi.yaml 出现，
// 且路径模板（{id} / {ns} …）与 HTTP 方法一致；反向亦然（文档里没有实现的接口一律删掉）。
func TestOpenAPICoversRoutes(t *testing.T) {
	var spec struct {
		OpenAPI string                            `json:"openapi"`
		Paths   map[string]map[string]interface{} `json:"paths"`
	}
	if err := yaml.Unmarshal(openapiSpec, &spec); err != nil {
		t.Fatalf("openapi.yaml 不是合法 YAML: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatalf("openapi 版本 %q", spec.OpenAPI)
	}

	documented := map[string]bool{}
	for p, ops := range spec.Paths {
		for m := range ops {
			switch m {
			case "get", "post", "put", "patch", "delete":
				documented[strings.ToUpper(m)+" /api/v1"+p] = true
			}
		}
	}

	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.HandleFunc\("(GET|POST|PUT|PATCH|DELETE) (/api/v1/[^"]+)"`)
	implemented := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		implemented[m[1]+" "+m[2]] = true
	}
	implemented["POST /api/v1/agents/register"] = true // mux.Handle("POST "+agenthub.RegisterPath, …)
	if len(implemented) < 20 {
		t.Fatalf("只扫到 %d 条路由，正则可能失效", len(implemented))
	}

	for r := range implemented {
		if !documented[r] {
			t.Errorf("路由未写进 openapi.yaml: %s", r)
		}
	}
	for r := range documented {
		if !implemented[r] {
			t.Errorf("openapi.yaml 里有但未实现: %s", r)
		}
	}
}
