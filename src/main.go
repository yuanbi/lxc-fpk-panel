package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed www
var wwwFS embed.FS

const appName = "LXCPanel"

var appVersion = "1.0.13"

// Config is persisted in ${TRIM_PKGVAR}/config.json
type Config struct {
	Port       int    `json:"port"`
	AllowLAN   bool   `json:"allow_lan"`
	LanTrust   bool   `json:"lan_trust"`
	APIToken   string `json:"api_token"`
	ImageHost  string `json:"image_host"`
	SampleSecs int    `json:"sample_secs"`
	// StorageDir 为空表示使用系统默认的 /var/lib/lxc。
	StorageDir string `json:"storage_dir"`
	// CacheDir 是 lxc-download 的镜像缓存目录，为空表示默认 /var/cache/lxc。
	CacheDir string `json:"cache_dir"`
	// ProxyURL 是给容器用的出网代理（如 http://10.0.3.1:7890）。为空表示不配。
	// 国内网络访问 GitHub 一类站点会在 TLS 握手被重置，配个代理最省事。
	ProxyURL string `json:"proxy_url"`
}

type App struct {
	mu      sync.RWMutex
	cfg     Config
	cfgPath string
	dataDir string

	sampler *Sampler
	tasks   *TaskManager
	terms   *TermManager
	admin   *AdminStore
	limiter *loginLimiter
	ports   *PortStore

	envMu    sync.Mutex
	envCache LXCEnv
	envAt    time.Time

	hostMu  sync.Mutex
	hostIPs map[string]bool
	hostAt  time.Time
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return "lxc_" + hex.EncodeToString(b)
}

func (a *App) loadConfig() error {
	def := Config{
		Port:       18080,
		AllowLAN:   true,
		LanTrust:   true,
		APIToken:   randomToken(),
		ImageHost:  "images.linuxcontainers.org",
		SampleSecs: 10,
	}
	raw, err := os.ReadFile(a.cfgPath)
	if err == nil {
		c := def
		if e := json.Unmarshal(raw, &c); e != nil {
			log.Printf("config parse failed, using defaults: %v", e)
			c = def
		}
		a.cfg = c
	} else {
		a.cfg = def
	}
	if a.cfg.APIToken == "" {
		a.cfg.APIToken = randomToken()
	}
	if a.cfg.Port <= 0 || a.cfg.Port > 65535 {
		a.cfg.Port = 18080
	}
	if a.cfg.SampleSecs < 3 {
		a.cfg.SampleSecs = 10
	}
	if a.cfg.ImageHost == "" {
		a.cfg.ImageHost = "images.linuxcontainers.org"
	}
	if p, err := normalizeStorageDir(a.cfg.StorageDir); err != nil {
		log.Printf("storage_dir %q 无效，回退到默认目录: %v", a.cfg.StorageDir, err)
		a.cfg.StorageDir = ""
	} else if p == defaultLXCStateDir {
		a.cfg.StorageDir = ""
	} else {
		a.cfg.StorageDir = p
	}
	setLXCPath(a.cfg.StorageDir)
	if p, err := normalizeCacheDir(a.cfg.CacheDir); err != nil {
		log.Printf("cache_dir %q 无效，回退到默认目录: %v", a.cfg.CacheDir, err)
		a.cfg.CacheDir = ""
	} else if p == defaultLXCCacheDir {
		a.cfg.CacheDir = ""
	} else {
		a.cfg.CacheDir = p
	}
	setLXCCacheDir(a.cfg.CacheDir)
	return a.saveConfig()
}

func (a *App) saveConfig() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.cfgPath)
}

func (a *App) getConfig() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

// tokenFilePath is the on-disk copy of the API token. It exists so an admin who
// cannot get into the panel (e.g. access from a network segment that is not
// trusted) can still read the token over SSH instead of reinstalling.
func (a *App) tokenFilePath() string {
	return filepath.Join(a.dataDir, "api_token.txt")
}

func (a *App) writeTokenFile() {
	p := a.tokenFilePath()
	if err := os.WriteFile(p, []byte(a.getConfig().APIToken+"\n"), 0o644); err != nil {
		log.Printf("write token file %s: %v", p, err)
	}
}

// ---------- auth ----------

func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host == "localhost"
	}
	return ip.IsLoopback()
}

func peerHost(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return host
}

// isPrivateIP matches RFC1918 / CGNAT / IPv6 ULA — i.e. "same box or same LAN".
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 — CGNAT, also used by Tailscale
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}

// localAddrs returns the set of IPs owned by this host. A reverse proxy living
// on the NAS itself connects from one of these, which is how the fnOS gateway
// request is recognised as "local" instead of "remote".
func (a *App) localAddrs() map[string]bool {
	a.hostMu.Lock()
	defer a.hostMu.Unlock()
	if a.hostIPs != nil && time.Since(a.hostAt) < 60*time.Second {
		return a.hostIPs
	}
	set := map[string]bool{"127.0.0.1": true, "::1": true}
	if ifaces, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range ifaces {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil {
				set[ip.String()] = true
			}
		}
	}
	a.hostIPs, a.hostAt = set, time.Now()
	return set
}

type peerKind int

const (
	peerRemote peerKind = iota // 真正的外部地址
	peerLAN                    // 同一局域网内的其它设备
	peerLocal                  // 本机进程（含本机反代 / 应用网关）
)

// classifyPeer decides where a request comes from. The fnOS gateway may reach
// the app either through loopback or through the NAS's own LAN address, and it
// may also use a unix socket (where RemoteAddr is not an IP at all).
func (a *App) classifyPeer(r *http.Request) peerKind {
	host := peerHost(r.RemoteAddr)
	ip := net.ParseIP(host)
	if ip == nil {
		// Not an IP => unix domain socket => a local proxy. Only trust it when a
		// proxy header proves it, otherwise treat as remote.
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-Ip") != "" || r.Header.Get("X-Real-IP") != "" {
			return peerLocal
		}
		if host == "localhost" {
			return peerLocal
		}
		return peerRemote
	}
	if ip.IsLoopback() {
		return peerLocal
	}
	if a.localAddrs()[ip.String()] {
		return peerLocal
	}
	if isPrivateIP(ip) {
		return peerLAN
	}
	return peerRemote
}

func tokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("X-API-Token"); h != "" {
		return strings.TrimSpace(h)
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if q := r.URL.Query().Get("token"); q != "" {
		return q
	}
	if c, err := r.Cookie("lxcpanel_token"); err == nil {
		return c.Value
	}
	return ""
}

func (a *App) tokenOK(r *http.Request) bool {
	tok := tokenFromRequest(r)
	if tok == "" {
		return false
	}
	want := a.getConfig().APIToken
	if len(tok) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1
}

// stripAppPrefix tolerates reverse proxies that keep the /app/<name> prefix,
// so both /api/x and /app/LXCPanel/api/x resolve to the same handler.
func stripAppPrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/app/") {
			rest := strings.TrimPrefix(p, "/app/")
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				r2 := r.Clone(r.Context())
				r2.URL.Path = rest[i:]
				next.ServeHTTP(w, r2)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authFree 这些路径自行鉴权（登录 / 设置密码 / 状态查询），不经过中间件拦截。
func authFree(p string) bool {
	switch p {
	case "/api/auth/login", "/api/auth/setup", "/api/auth/state", "/api/auth/logout":
		return true
	}
	return false
}

func (a *App) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.getConfig()
		if authFree(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// 1) 浏览器会话（密码登录后签发，30 天有效）
		if a.sessionOK(r) {
			next.ServeHTTP(w, r)
			return
		}

		// 2) API Token（脚本 / 自动化调用）
		if a.tokenOK(r) {
			next.ServeHTTP(w, r)
			return
		}

		// 3) 本机进程（含飞牛应用网关反代），以及可选的同内网免密
		kind := a.classifyPeer(r)
		if kind == peerLocal || (kind == peerLAN && cfg.LanTrust && cfg.AllowLAN) {
			next.ServeHTTP(w, r)
			return
		}

		if !cfg.AllowLAN {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "局域网访问已关闭：请在飞牛应用窗口的「设置」中开启，或使用 API Token 访问",
			})
			return
		}

		// 4) 未认证
		if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
			a.serveGate(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "未登录：请打开面板窗口登录，或携带 X-API-Token 请求头",
		})
	})
}

func main() {
	var (
		port     = flag.Int("port", 0, "listen port (overrides config)")
		dataDir  = flag.String("data", "", "data directory")
		www      = flag.String("www", "", "static UI directory")
		printV   = flag.Bool("version", false, "print version")
		setPass  = flag.Bool("set-pass", false, "set admin password from stdin and exit")
		passArg  = flag.String("pass", "", "admin password (use with -set-pass)")
		lanTrust = flag.String("lan-trust", "", "true/false: skip password for LAN peers")
	)
	flag.Parse()
	// systemd 拉起的服务 PATH 通常没有 sbin，iptables / dnsmasq / chpasswd 这类
	// 工具都在那里；补上以免「明明装了却提示找不到」。
	ensurePath()
	if *printV {
		fmt.Printf("%s %s\n", appName, appVersion)
		return
	}

	dir := *dataDir
	if dir == "" {
		dir = envOr("TRIM_PKGVAR", "")
	}
	if dir == "" {
		dir = envOr("LXCPANEL_DATA", "./var")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	for _, sub := range []string{"tasks", "meta"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	app := &App{dataDir: dir, cfgPath: filepath.Join(dir, "config.json")}
	appDataDir = dir
	if err := app.loadConfig(); err != nil {
		log.Fatalf("load config: %v", err)
	}
	if *port > 0 {
		app.cfg.Port = *port
	}
	app.admin = NewAdminStore(filepath.Join(dir, "admin.json"))
	app.limiter = newLoginLimiter()

	// 安装向导把密码通过环境变量交给 cmd/install_callback，后者以 -set-pass 调起本程序。
	if *setPass {
		pw := *passArg
		if pw == "" {
			if raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096)); err == nil {
				pw = strings.TrimRight(string(raw), "\r\n")
			}
		}
		if strings.TrimSpace(pw) == "" {
			fmt.Fprintln(os.Stderr, "密码为空，未做修改")
			os.Exit(1)
		}
		app.admin.Load() // 确保 admin.json 与会话密钥存在
		if err := app.admin.SetPassword(pw); err != nil {
			fmt.Fprintf(os.Stderr, "保存密码失败: %v\n", err)
			os.Exit(1)
		}
		if *lanTrust != "" {
			app.mu.Lock()
			app.cfg.LanTrust = *lanTrust == "true" || *lanTrust == "1"
			app.mu.Unlock()
			_ = app.saveConfig()
		}
		fmt.Println("管理密码已更新:", app.adminFilePath())
		return
	}

	metaStore := NewMetaStore(filepath.Join(dir, "meta", "containers.json"))
	app.sampler = NewSampler(metaStore, time.Duration(app.getConfig().SampleSecs)*time.Second)
	app.sampler.Start()
	app.tasks = NewTaskManager(filepath.Join(dir, "tasks"))
	app.terms = NewTermManager()
	app.ports = NewPortStore(filepath.Join(dir, "portmap.json"))

	// 面板重启后 iptables 规则通常已经丢失，启动几秒后按存档重建一次。
	go func() {
		time.Sleep(3 * time.Second)
		if err := app.syncPorts(nil); err != nil {
			log.Printf("重建端口转发规则失败: %v", err)
		}
		// 开机后 lxc 桥接 / NAT / DHCP 可能还没就绪（lxc-net 未启用），顺手兜一层，
		// 否则容器「有网卡没网络」。
		if logs, err := hostNetFix(nil); err != nil {
			log.Printf("宿主机网络自检未完全成功: %v", err)
		} else if len(logs) > 0 {
			log.Printf("宿主机网络自检: %s", strings.Join(logs, "；"))
		}
		// 配了容器代理的话，重启后也要补回容器里（这些文件在容器的 rootfs，持久化没问题，
		// 这里主要覆盖「配了代理但当时容器停着」的情况）
		if p := app.proxyURL(); p != "" {
			var plogs []string
			applyProxyToRunning(p, func(s string) { plogs = append(plogs, s) })
			if len(plogs) > 0 {
				log.Printf("容器代理: %s", strings.Join(plogs, "；"))
			}
		}
	}()

	app.writeTokenFile()
	adm := app.admin.Load()
	if adm.Password == "" {
		log.Printf("管理密码尚未设置（可在面板窗口内设置，%s 内有效）", setupGrace)
	} else {
		log.Printf("管理密码已就绪（%s）", app.adminFilePath())
	}
	log.Printf("API token: %s (also saved to %s)", app.getConfig().APIToken, app.tokenFilePath())

	mux := http.NewServeMux()
	app.registerAPI(mux)

	// 优先使用安装目录下的 www（可直接修改，无需重新打包），否则回退到内嵌资源
	var static fs.FS
	wwwDir := *www
	if wwwDir == "" {
		if dest := envOr("TRIM_APPDEST", ""); dest != "" {
			wwwDir = filepath.Join(dest, "www")
		}
	}
	if wwwDir != "" {
		if st, err := os.Stat(wwwDir); err == nil && st.IsDir() {
			static = os.DirFS(wwwDir)
			log.Printf("serving UI from disk: %s", wwwDir)
		}
	}
	if static == nil {
		sub, err := fs.Sub(wwwFS, "www")
		if err != nil {
			log.Fatalf("static fs: %v", err)
		}
		static = sub
	}
	mux.Handle("/", staticHandler(static))

	addr := fmt.Sprintf("0.0.0.0:%d", app.cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           stripAppPrefix(app.withAuth(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("%s %s listening on %s (data: %s)", appName, appVersion, addr, dir)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// serveGate 渲染未认证访问的入口页：首次使用引导设置密码，其余情况要求登录。
func (a *App) serveGate(w http.ResponseWriter, r *http.Request) {
	f := a.admin.Load()
	mode := "login"
	if f.Password == "" {
		if a.setupAllowed(r) {
			mode = "setup"
		} else {
			mode = "locked"
		}
	}
	cmd := fmt.Sprintf("sudo %s -set-pass -data %s", a.selfPath(), a.dataDir)
	page := strings.ReplaceAll(gateTpl, "{{BRAND}}", appName)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(renderGate(page, mode, cmd)))
}

// setupAllowed 未设置密码时，允许在同一台机器上或安装后的宽限期内通过界面设置。
func (a *App) setupAllowed(r *http.Request) bool {
	if a.classifyPeer(r) == peerLocal {
		return true
	}
	return time.Since(time.Unix(startedAt, 0)) < setupGrace
}

func (a *App) selfPath() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return "/vol1/@appcenter/LXCPanel/server/lxcpanel-amd64"
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func renderGate(tpl, mode, cmd string) string {
	var title, desc, extra, btn, action, hint string
	pinput := `<input id="p" type="password" placeholder="管理密码" autocomplete="current-password" autofocus>`
	pbutton := ""
	switch mode {
	case "setup":
		title = "设置管理密码"
		desc = "首次使用，请为面板设置一个管理密码。密码会以 PBKDF2 哈希保存在设备上，忘记后可在安装向导或命令行重置。"
		extra = `<input id="p2" type="password" placeholder="再输入一次密码" autocomplete="new-password">`
		btn = "设置并进入"
		action = "api/auth/setup"
		hint = "密码至少 6 位，建议 12 位以上并包含数字与字母。"
	case "locked":
		title = "需要先设置管理密码"
		desc = "尚未设置管理密码，且已超出界面自助设置时限。请在服务器 SSH 执行以下命令设置密码："
		extra = ""
		btn = ""
		action = ""
		hint = `<div class="cmd">` + htmlEscape(cmd) + `</div>`
		pinput = ""
	default:
		title = "登录 LXC 容器管理"
		desc = "请输入管理密码。密码在安装应用时设置，登录状态会在此设备保留 30 天。"
		extra = ""
		btn = "登录"
		action = "api/auth/login"
		hint = "忘记密码？在服务器执行 <b>-set-pass</b> 命令可重置，详见文档。"
	}
	if pbutton == "" {
		if btn != "" {
			pbutton = `<button type="submit" id="btn">` + btn + `</button>`
		}
	}
	out := strings.NewReplacer(
		"{{TITLE}}", title,
		"{{DESC}}", desc,
		"{{EXTRA}}", extra,
		"{{BTN}}", btn,
		"{{ACTION}}", action,
		"{{HINT}}", hint,
		"{{MODE}}", mode,
		"{{PINPUT}}", pinput,
		"{{PBUTTON}}", pbutton,
	).Replace(tpl)
	return out
}

const gateTpl = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{BRAND}}</title>
<style>
  *{box-sizing:border-box}
  body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;
       font-family:system-ui,-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;
       background:radial-gradient(1200px 620px at 22% -10%,#e6efff,#f6f8fc 58%);color:#0f172a}
  .card{width:min(94vw,392px);background:#fff;border-radius:20px;padding:30px 28px 26px;
        box-shadow:0 24px 60px rgba(15,23,42,.13);border:1px solid #e8ecf3}
  .logo{width:40px;height:40px;border-radius:12px;background:linear-gradient(135deg,#3b82f6,#7c5cff);
        display:grid;place-items:center;margin-bottom:18px;box-shadow:0 8px 18px rgba(59,130,246,.32)}
  .logo svg{width:22px;height:22px}
  h1{margin:0 0 8px;font-size:19px;letter-spacing:-.2px}
  p.desc{margin:0 0 20px;color:#64748b;font-size:13px;line-height:1.65}
  input{width:100%;padding:11px 13px;border-radius:11px;border:1px solid #d7dde7;font-size:14px;
        outline:none;margin-bottom:10px;background:#fbfcfe;transition:border-color .15s,box-shadow .15s}
  input:focus{border-color:#2563eb;background:#fff;box-shadow:0 0 0 3px rgba(37,99,235,.14)}
  button{width:100%;padding:11px;border:0;border-radius:11px;background:#2563eb;color:#fff;
         font-size:14px;font-weight:600;cursor:pointer;margin-top:4px;transition:background .15s}
  button:hover{background:#1d4ed8}
  button:disabled{opacity:.6;cursor:default}
  .err{margin-top:12px;color:#dc2626;font-size:13px;min-height:17px}
  .hint{margin-top:14px;color:#94a3b8;font-size:12px;line-height:1.7}
  .hint b{color:#64748b}
  .cmd{margin-top:10px;background:#0f172a;color:#e2e8f0;border-radius:10px;padding:11px 12px;
       font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px;
       line-height:1.6;word-break:break-all}
</style></head>
<body><form class="card" onsubmit="return submitForm(event)">
  <div class="logo"><svg viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2"
      stroke-linecap="round" stroke-linejoin="round"><path d="m12 3 8 4.5-8 4.5-8-4.5L12 3Z"/>
      <path d="m4 12.2 8 4.5 8-4.5M4 16.6l8 4.5 8-4.5"/></svg></div>
  <h1>{{TITLE}}</h1>
  <p class="desc">{{DESC}}</p>
  {{PINPUT}}
  {{EXTRA}}
  {{PBUTTON}}
  <div class="err" id="e"></div>
  <div class="hint">{{HINT}}</div>
</form>
<script>
const MODE = "{{MODE}}";
async function submitForm(ev){
  ev.preventDefault();
  const p = document.getElementById('p').value;
  const p2 = document.getElementById('p2');
  const err = document.getElementById('e');
  const btn = document.getElementById('btn');
  err.textContent = '';
  if (!p) { err.textContent = '请输入密码'; return false; }
  if (p2 && p !== p2.value) { err.textContent = '两次输入的密码不一致'; return false; }
  if (MODE === 'setup' && p.length < 6) { err.textContent = '密码至少 6 位'; return false; }
  btn.disabled = true;
  try {
    const r = await fetch('{{ACTION}}', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({password: p})
    });
    const data = await r.json().catch(() => ({}));
    if (r.ok) { location.replace(location.pathname); return false; }
    err.textContent = data.error || '操作失败，请重试';
  } catch (_) {
    err.textContent = '网络错误，请重试';
  }
  btn.disabled = false;
  return false;
}
</script></body></html>
`

// ensurePath 把常见的系统 bin/sbin 目录补进 PATH，避免服务环境下找不到
// iptables / dnsmasq / ss / chpasswd 等工具。
func ensurePath() {
	want := []string{"/usr/sbin", "/sbin", "/usr/local/sbin", "/usr/bin", "/bin", "/usr/local/bin"}
	cur := os.Getenv("PATH")
	parts := strings.Split(cur, ":")
	have := map[string]bool{}
	for _, p := range parts {
		have[p] = true
	}
	var add []string
	for _, w := range want {
		if !have[w] {
			add = append(add, w)
		}
	}
	if len(add) == 0 {
		return
	}
	np := cur
	if np != "" {
		np += ":"
	}
	np += strings.Join(add, ":")
	os.Setenv("PATH", np)
}
