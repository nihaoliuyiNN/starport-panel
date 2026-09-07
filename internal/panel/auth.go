package panel

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/cluster"
)

// actorKey 请求上下文里的调用者身份（供审计）。
type actorKey struct{}

// Actor 通过鉴权的调用者。
type Actor struct {
	TokenID   uint64 // 库内令牌 ID；静态令牌 / 关闭鉴权时为 0
	TokenName string // 库内令牌名；静态令牌 "static"；关闭鉴权 "anonymous"
}

func actorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{TokenName: "anonymous"}
}

// auth API 鉴权中间件。凭据来源（任一）：
//   - Header `Authorization: Bearer <token>`
//   - 查询参数 `?token=<token>`（浏览器 WebSocket 无法自定义 Header，仅为此保留）
//
// 令牌校验：先比静态 Config.APIToken，再查库（api_tokens，未吊销）。
// 只保护 /api/**；静态 UI 资源、/healthz 与 agent 引导注册（自有 bootstrap token）放行。
// 通过后把调用者身份放进 ctx（actorFrom），审计中间件据此记录。
func (s *Server) auth(next http.Handler) http.Handler {
	if s.cfg.InsecureNoAuth {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || (r.Method == http.MethodPost && r.URL.Path == agenthub.RegisterPath) {
			next.ServeHTTP(w, r)
			return
		}
		tok := bearer(r)
		if tok == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="starport-panel"`)
			writeErr(w, &cluster.Error{Code: "UNAUTHENTICATED", Message: "缺少 API 令牌（Authorization: Bearer <token>）", Status: 401})
			return
		}
		actor, ok := s.resolveToken(r, tok)
		if !ok {
			writeErr(w, &cluster.Error{Code: "UNAUTHENTICATED", Message: "API 令牌无效或已吊销", Status: 401})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

func (s *Server) resolveToken(r *http.Request, tok string) (Actor, bool) {
	if s.cfg.APIToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.APIToken)) == 1 {
		return Actor{TokenName: "static"}, true
	}
	t, err := s.store.AuthenticateToken(r.Context(), tok)
	if err != nil {
		return Actor{}, false
	}
	return Actor{TokenID: t.ID, TokenName: t.Name}, true
}

func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return r.URL.Query().Get("token")
}
