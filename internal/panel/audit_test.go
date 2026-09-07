package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"starport-panel/internal/panel/agenthub"
)

func TestAuditMiddleware(t *testing.T) {
	s, dbToken := newAuthServer(t, Config{APIToken: "static-secret"})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/fail" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	h := s.auth(s.audit(inner))

	do := func(method, path, token string) {
		req := httptest.NewRequest(method, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("X-Forwarded-For", "10.1.2.3, 172.16.0.1")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	do("GET", "/api/v1/nodes", dbToken)           // 只读：不记
	do("POST", "/api/v1/nodes/1/exec", dbToken)   // 记，库内令牌
	do("DELETE", "/api/v1/fail", "static-secret") // 记，静态令牌，409
	do("POST", "/api/v1/clusters", "bad")         // 401 被 auth 挡住：不记
	do("POST", agenthub.RegisterPath, "")         // agent 注册：不记
	do("POST", "/assets/x", "")                   // 非 /api：不记

	es, err := s.store.ListAudit(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(es), es)
	}
	// 降序：最后写的在前
	if es[0].Method != "DELETE" || es[0].Path != "/api/v1/fail" || es[0].Status != 409 || es[0].TokenName != "static" || es[0].TokenID != 0 {
		t.Errorf("entry0 = %+v", es[0])
	}
	if es[1].Method != "POST" || es[1].Path != "/api/v1/nodes/1/exec" || es[1].Status != 202 || es[1].TokenName != "test" || es[1].TokenID == 0 {
		t.Errorf("entry1 = %+v", es[1])
	}
	if es[1].RemoteIP != "10.1.2.3" {
		t.Errorf("remote ip = %q", es[1].RemoteIP)
	}

	// 翻页：before = 最新 ID → 只剩旧的一条
	older, _ := s.store.ListAudit(context.Background(), es[0].ID, 10)
	if len(older) != 1 || older[0].ID != es[1].ID {
		t.Errorf("paging: %+v", older)
	}
}
