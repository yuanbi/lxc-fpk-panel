package main

import (
	"io/fs"
	"net/http"
	"strings"
)

// assetFiles 是 index.html 里引用到的本地资源。
// 升级应用后如果浏览器继续使用旧缓存，会出现"点了没反应 / 打开还是旧页面"，
// 因此在服务端给这些 URL 附加版本号，强制刷新。
var assetFiles = []string{
	"app.css",
	"vendor/xterm.css",
	"vendor/xterm.js",
	"vendor/addon-fit.js",
	"app.js",
}

// stampAssets 把 index.html 中的资源引用改写为 "<file>?v=<appVersion>"。
func stampAssets(html string) string {
	suffix := "?v=" + appVersion
	for _, f := range assetFiles {
		// 只改写没有查询串的原始引用，避免重复追加。
		for _, q := range []string{`"`, `'`} {
			html = strings.ReplaceAll(html, q+f+q, q+f+suffix+q)
		}
	}
	return html
}

// staticHandler 提供前端资源：
//   - index.html（及任意未命中的路径，相当于 SPA 兜底）每次都不缓存，并注入资源版本号；
//   - 其它资源一律 no-cache，浏览器每次都做条件请求，升级后立即生效。
func staticHandler(static fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(static))
	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		raw, err := fs.ReadFile(static, "index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(stampAssets(string(raw))))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" || strings.HasSuffix(p, "index.html") {
			serveIndex(w, r)
			return
		}
		if st, err := fs.Stat(static, p); err != nil || st.IsDir() {
			serveIndex(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}
