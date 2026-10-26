package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// 资源引用必须带上版本号，否则升级后浏览器会继续用旧 app.js。
func TestStampAssets(t *testing.T) {
	const in = `<link rel="stylesheet" href="app.css" />
<script src="vendor/xterm.js"></script>
<script src="app.js"></script>`
	out := stampAssets(in)

	for _, want := range []string{
		`href="app.css?v=` + appVersion + `"`,
		`src="vendor/xterm.js?v=` + appVersion + `"`,
		`src="app.js?v=` + appVersion + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少版本标记 %s\n输出: %s", want, out)
		}
	}

	// 二次处理不应重复追加版本号
	if again := stampAssets(out); again != out {
		t.Errorf("重复改写产生了叠加版本号: %s", again)
	}
}

func TestStaticHandlerHeaders(t *testing.T) {
	mfs := fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte(`<script src="app.js"></script>`)},
		"app.js":          &fstest.MapFile{Data: []byte("console.log(1)")},
		"vendor/xterm.js": &fstest.MapFile{Data: []byte("x")},
	}
	h := staticHandler(mfs)

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	// 首页：不缓存 + 资源带版本号
	w := get("/")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("首页应 no-store, got %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if !strings.Contains(w.Body.String(), "app.js?v="+appVersion) {
		t.Errorf("首页资源未注入版本号: %s", w.Body.String())
	}

	// 静态资源：no-cache（允许 304 复用，但必须校验）
	if w = get("/app.js"); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("资源应 no-cache, got %d %q", w.Code, w.Header().Get("Cache-Control"))
	}

	// 深链兜底：未知路径返回首页
	if w = get("/some/deep/route"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "app.js") {
		t.Fatalf("SPA 兜底失败: %d", w.Code)
	}
}
