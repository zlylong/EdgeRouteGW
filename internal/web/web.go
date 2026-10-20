// Package web embeds the built frontend. During M0 it is a placeholder page;
// from M2 the Vite build writes its output into dist/ before go build.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// placeholder is served when the binary was built without "make web".
const placeholder = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>EdgeRouteGW</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;font-family:system-ui,sans-serif;background:#0f172a;color:#e2e8f0}code{background:#1e293b;padding:.2em .5em;border-radius:.3em}p{color:#94a3b8}</style></head>
<body><main style="text-align:center"><h1>EdgeRouteGW 2.0</h1><p>后端已启动,但此二进制未包含前端。请执行 <code>make web build</code>。</p><p><code>GET /api/v2/state</code></p></main></body></html>`

// Handler serves the embedded SPA. Unknown paths fall back to index.html so
// client-side routes survive a reload.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		index = []byte(placeholder)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			serveIndex(w, index)
			return
		}
		if f, err := sub.Open(p); err == nil {
			_ = f.Close()
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, index)
	})
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(index)
}
