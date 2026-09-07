package panel

import (
	_ "embed"
	"net/http"
)

// openapiSpec 公开 API 的 OpenAPI 3.1 描述，随二进制提供（GET /api/v1/openapi.yaml）。
// 改接口时同步改它；docs/api.md 是同一内容的人读版。
//
//go:embed openapi.yaml
var openapiSpec []byte

func (s *Server) openapi(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(openapiSpec)
}
