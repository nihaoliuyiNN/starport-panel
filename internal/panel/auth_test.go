package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/store"
)

func newAuthServer(t *testing.T, cfg Config) (*Server, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, plain, err := st.CreateToken(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: cfg, store: st}, plain
}

func TestAuthMiddleware(t *testing.T) {
	s, dbToken := newAuthServer(t, Config{APIToken: "static-secret"})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.auth(ok)

	do := func(method, path string, hdr map[string]string) int {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	cases := []struct {
		name   string
		method string
		path   string
		hdr    map[string]string
		want   int
	}{
		{"no token", "GET", "/api/v1/nodes", nil, 401},
		{"bad token", "GET", "/api/v1/nodes", map[string]string{"Authorization": "Bearer nope"}, 401},
		{"db token", "GET", "/api/v1/nodes", map[string]string{"Authorization": "Bearer " + dbToken}, 200},
		{"static token", "GET", "/api/v1/nodes", map[string]string{"Authorization": "bearer static-secret"}, 200},
		{"query token", "GET", "/api/v1/nodes/1/terminal?token=" + dbToken, nil, 200},
		{"healthz open", "GET", "/healthz", nil, 200},
		{"ui open", "GET", "/clusters/1", nil, 200},
		{"ui assets open", "GET", "/assets/index-abc.js", nil, 200},
		{"api prefix protected", "GET", "/api/v1/version", nil, 401},
		{"register open", "POST", agenthub.RegisterPath, nil, 200},
		{"register GET not open", "GET", agenthub.RegisterPath, nil, 401},
		{"basic scheme rejected", "GET", "/api/v1/nodes", map[string]string{"Authorization": "Basic abc"}, 401},
	}
	for _, tc := range cases {
		if got := do(tc.method, tc.path, tc.hdr); got != tc.want {
			t.Errorf("%s: want %d got %d", tc.name, tc.want, got)
		}
	}

	// 吊销后立即失效
	ts, _ := s.store.ListTokens(context.Background())
	_ = s.store.RevokeToken(context.Background(), ts[0].ID)
	if got := do("GET", "/api/v1/nodes", map[string]string{"Authorization": "Bearer " + dbToken}); got != 401 {
		t.Errorf("revoked: got %d", got)
	}

	// 关闭鉴权
	s.cfg.InsecureNoAuth = true
	if got := func() int {
		rec := httptest.NewRecorder()
		s.auth(ok).ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/nodes", nil))
		return rec.Code
	}(); got != 200 {
		t.Errorf("no-auth: got %d", got)
	}
}
