package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a := &App{dataDir: dir, cfgPath: filepath.Join(dir, "config.json")}
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	a.admin = NewAdminStore(filepath.Join(dir, "admin.json"))
	a.limiter = newLoginLimiter()
	return a
}

func req(remote string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestClassifyPeer(t *testing.T) {
	a := newTestApp(t)
	// 取一个本机真实网卡地址，模拟"NAS 上的反代连自己"
	var own string
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, ad := range addrs {
			if ipnet, ok := ad.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if v4 := ipnet.IP.To4(); v4 != nil {
					own = v4.String()
					break
				}
			}
		}
	}

	cases := []struct {
		name   string
		remote string
		header map[string]string
		want   peerKind
	}{
		{"loopback", "127.0.0.1:5555", nil, peerLocal},
		{"loopback-v6", "[::1]:5555", nil, peerLocal},
		{"lan-other", "192.168.1.50:5555", nil, peerLAN},
		{"lan-10", "10.0.0.7:5555", nil, peerLAN},
		{"cgnat", "100.64.3.9:5555", nil, peerLAN},
		{"public", "8.8.8.8:5555", nil, peerRemote},
		{"unix-socket-no-header", "@", nil, peerRemote},
		{"unix-socket-with-header", "@", map[string]string{"X-Real-IP": "192.168.1.9"}, peerLocal},
	}
	if own != "" {
		cases = append(cases, struct {
			name   string
			remote string
			header map[string]string
			want   peerKind
		}{"own-iface", own + ":5555", nil, peerLocal})
	}

	for _, c := range cases {
		if got := a.classifyPeer(req(c.remote, c.header)); got != c.want {
			t.Errorf("%s: classifyPeer(%q)=%v want %v", c.name, c.remote, got, c.want)
		}
	}
}

func TestAuthMatrix(t *testing.T) {
	a := newTestApp(t)
	h := a.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	do := func(remote, path, token string) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = remote
		if token != "" {
			r.Header.Set("X-API-Token", token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	tok := a.getConfig().APIToken

	// 默认：内网免 Tootn，网关/本机直通，公网必须 Token
	if c := do("192.168.1.50:1", "/", ""); c != http.StatusOK {
		t.Errorf("lan default should pass, got %d", c)
	}
	if c := do("8.8.8.8:1", "/", ""); c != http.StatusUnauthorized {
		t.Errorf("public without token should be 401, got %d", c)
	}
	if c := do("8.8.8.8:1", "/", tok); c != http.StatusOK {
		t.Errorf("public with token should pass, got %d", c)
	}
	if c := do("8.8.8.8:1", "/api/containers", ""); c != http.StatusUnauthorized {
		t.Errorf("public api without token should be 401, got %d", c)
	}

	// 关闭内网免密：内网非本机必须 Token；本机仍然放行
	a.cfg.LanTrust = false
	if c := do("192.168.1.50:1", "/", ""); c != http.StatusUnauthorized {
		t.Errorf("lan with trust off should be 401, got %d", c)
	}
	if c := do("127.0.0.1:1", "/", ""); c != http.StatusOK {
		t.Errorf("loopback must always pass, got %d", c)
	}
	if c := do("192.168.1.50:1", "/", tok); c != http.StatusOK {
		t.Errorf("lan with token should pass, got %d", c)
	}

	// 关闭局域网访问：内网无 Token 一律 403；持有 Token 仍可用（自动化脚本）
	a.cfg.AllowLAN = false
	if c := do("192.168.1.50:1", "/", ""); c != http.StatusForbidden {
		t.Errorf("lan disabled without token should be 403, got %d", c)
	}
	if c := do("192.168.1.50:1", "/", tok); c != http.StatusOK {
		t.Errorf("lan disabled with token should pass, got %d", c)
	}
	if c := do("127.0.0.1:1", "/", ""); c != http.StatusOK {
		t.Errorf("loopback must always pass, got %d", c)
	}
}

func TestTokenFileWritten(t *testing.T) {
	a := newTestApp(t)
	a.writeTokenFile()
	raw, err := os.ReadFile(a.tokenFilePath())
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	if string(raw) != a.getConfig().APIToken+"\n" {
		t.Fatalf("token file content mismatch: %q", string(raw))
	}
}

// 用伪造的 RemoteAddr 走 ServeHTTP，模拟"公网来源"（不可信）的访问。
func testHandler(a *App) http.Handler {
	mux := http.NewServeMux()
	a.registerAPI(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("PANEL"))
	})
	return stripAppPrefix(a.withAuth(mux))
}

func doReq(h http.Handler, method, path, remote, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.RemoteAddr = remote
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPasswordFlow(t *testing.T) {
	a := newTestApp(t)
	h := testHandler(a)
	const remote = "8.8.8.8:1234"

	// 1) 未设密码 + 不可信来源 → 显示"设置管理密码"
	w := doReq(h, http.MethodGet, "/", remote, "", nil)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "设置管理密码") {
		t.Fatalf("首次访问应显示设置密码页, got %d", w.Code)
	}

	// 2) 设置密码 → 签发会话
	w = doReq(h, http.MethodPost, "/api/auth/setup", remote, `{"password":"hunter2pass"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("setup 应成功, got %d %s", w.Code, w.Body.String())
	}
	var sess *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("设置密码后应签发会话 Cookie")
	}

	// 3) 带会话可进面板
	if w = doReq(h, http.MethodGet, "/", remote, "", []*http.Cookie{sess}); w.Code != http.StatusOK {
		t.Fatalf("带会话 cookie 应放行, got %d", w.Code)
	}

	// 4) 已有密码后再 setup 必须被拒
	if w = doReq(h, http.MethodPost, "/api/auth/setup", remote, `{"password":"another123"}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("重复设置密码应 403, got %d", w.Code)
	}

	// 5) 未登录访问 → 密码登录页
	if w = doReq(h, http.MethodGet, "/", remote, "", nil); w.Code != http.StatusUnauthorized ||
		!strings.Contains(w.Body.String(), "登录 LXC 容器管理") {
		t.Fatalf("应显示登录页, got %d", w.Code)
	}

	// 6) 错误密码 / 正确密码
	if w = doReq(h, http.MethodPost, "/api/auth/login", remote, `{"password":"wrong-pass"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401, got %d", w.Code)
	}
	if w = doReq(h, http.MethodPost, "/api/auth/login", remote, `{"password":"hunter2pass"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("正确密码应 200, got %d", w.Code)
	}

	// 7) 未登录调用 API 返回 401 JSON
	if w = doReq(h, http.MethodGet, "/api/containers", remote, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 API 应 401, got %d", w.Code)
	}
}

func TestPasswordHashRoundtrip(t *testing.T) {
	h, err := hashPassword("s3cret-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(h, "pbkdf2_sha256$") {
		t.Fatalf("unexpected format: %s", h)
	}
	if !verifyPassword("s3cret-pass", h) {
		t.Error("正确密码应校验通过")
	}
	if verifyPassword("s3cret-pass ", h) || verifyPassword("", h) {
		t.Error("错误密码不应通过")
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := newTestApp(t)
	if err := a.admin.SetPassword("correct-horse"); err != nil {
		t.Fatal(err)
	}
	h := testHandler(a)
	const remote = "7.7.7.7:1234"

	for i := 0; i < 6; i++ {
		doReq(h, http.MethodPost, "/api/auth/login", remote, `{"password":"nope"}`, nil)
	}
	w := doReq(h, http.MethodPost, "/api/auth/login", remote, `{"password":"correct-horse"}`, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("连续失败后应 429, got %d", w.Code)
	}
}

// 把入口页三种状态导出为 HTML，便于用无头浏览器做视觉检查（GATE_OUT 目录）。
func TestRenderGatePages(t *testing.T) {
	out := os.Getenv("GATE_OUT")
	if out == "" {
		t.Skip("GATE_OUT not set")
	}
	cmd := "sudo /vol1/@appcenter/LXCPanel/server/lxcpanel-amd64 -set-pass -data /vol1/@appdata/LXCPanel"
	for _, mode := range []string{"setup", "login", "locked"} {
		page := strings.ReplaceAll(gateTpl, "{{BRAND}}", appName)
		html := renderGate(page, mode, cmd)
		if strings.Contains(html, "{{") {
			t.Fatalf("%s: 仍有未替换的占位符", mode)
		}
		if err := os.WriteFile(filepath.Join(out, "gate_"+mode+".html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// 空切片必须序列化成 []，否则前端 .length 会抛异常导致整页白渲染。
// 这正是 1.0.2 及以前"点运行环境仍显示概览"的原因（设备上未装 LXC → templates 为 null）。
func TestEnvArraysNeverNull(t *testing.T) {
	raw, err := json.Marshal(normalizeEnv(LXCEnv{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"templates", "problems", "hints"} {
		if strings.Contains(string(raw), `"`+key+`":null`) {
			t.Errorf("%s 不应为 null: %s", key, raw)
		}
		if !strings.Contains(string(raw), `"`+key+`":[]`) {
			t.Errorf("%s 应为空数组: %s", key, raw)
		}
	}
}
