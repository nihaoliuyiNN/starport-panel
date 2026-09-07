package panel

import (
	"context"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/store"
)

// audit 记录 /api/** 下的写操作（POST / PUT / PATCH / DELETE）：谁（令牌）、何时、对哪个路径、结果状态码。
// 不记请求体。agent 注册（bootstrap token，非用户操作）与只读请求不记。
// 须放在 auth 之内（依赖 ctx 里的 Actor）。
func (s *Server) audit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || !isWrite(r.Method) || r.URL.Path == agenthub.RegisterPath {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		actor := actorFrom(r.Context())
		entry := store.AuditEntry{
			At:         start,
			TokenID:    actor.TokenID,
			TokenName:  actor.TokenName,
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     rec.status,
			RemoteIP:   remoteIP(r),
			DurationMs: time.Since(start).Milliseconds(),
		}
		// 用独立 ctx：请求已结束，r.Context() 可能已取消
		if err := s.store.AppendAudit(context.Background(), entry); err != nil {
			log.Printf("[panel] 审计写入失败: %v", err)
		}
	})
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// statusRecorder 捕获状态码；透传 Flush / Hijack 以免破坏流式日志与 WebSocket。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 让 http.ResponseController 拿到原始 writer（WebSocket 升级需要 Hijack）。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// remoteIP 取客户端 IP：优先 X-Forwarded-For 首项（面板常在反代后），否则 RemoteAddr。
func remoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// listAudit GET /api/v1/audit?before=<id>&limit=<n>
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseUint(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	es, err := s.store.ListAudit(r.Context(), before, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if es == nil {
		es = []store.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, es)
}
