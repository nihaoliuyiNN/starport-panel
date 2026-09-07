// Package ui 把 web/ 构建产物内嵌进面板二进制，提供单页应用的静态服务。
//
// 构建：`make ui`（pnpm build → 本包 dist/）。未构建时 dist 只有占位文件，访问根路径返回提示而不是 404。
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler 返回静态文件处理器：
//   - 命中文件直接返回（带哈希的 assets 长缓存）；
//   - 其它路径回退 index.html（前端路由）；
//   - 未构建（无 index.html）时返回 503 提示。
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FS(sub)
	_, statErr := fs.Stat(sub, "index.html")
	built := statErr == nil
	fileServer := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !built {
			http.Error(w, "starport-panel UI 未构建：请在仓库执行 `make ui` 后重新编译，或直接使用 /api/v1", http.StatusServiceUnavailable)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := sub.Open(p); err == nil {
			_ = f.Close()
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA 回退
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
