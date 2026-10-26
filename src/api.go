package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (a *App) registerAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/state", a.handleAuthState)
	mux.HandleFunc("/api/auth/login", a.handleAuthLogin)
	mux.HandleFunc("/api/auth/setup", a.handleAuthSetup)
	mux.HandleFunc("/api/auth/logout", a.handleAuthLogout)
	mux.HandleFunc("/api/auth/password", a.handleAuthPassword)
	mux.HandleFunc("/api/login", a.handleLogin)
	mux.HandleFunc("/api/overview", a.handleOverview)
	mux.HandleFunc("/api/system", a.handleSystem)
	mux.HandleFunc("/api/env", a.handleEnv)
	mux.HandleFunc("/api/env/install", a.handleEnvInstall)
	mux.HandleFunc("/api/distros", a.handleDistros)
	mux.HandleFunc("/api/containers", a.handleContainers)
	mux.HandleFunc("/api/containers/", a.handleContainerSub)
	mux.HandleFunc("/api/term/", a.handleTermRoute)
	mux.HandleFunc("/api/tasks/", a.handleTaskRoute)
	mux.HandleFunc("/api/tasks", a.handleTasksList)
	mux.HandleFunc("/api/settings", a.handleSettings)
	mux.HandleFunc("/api/storage/switch", a.handleStorageSwitch)
	mux.HandleFunc("/api/portmap", a.handlePortMap)
	mux.HandleFunc("/api/portmap/", a.handlePortMapSub)
	mux.HandleFunc("/api/hostnet", a.handleHostNet)
	mux.HandleFunc("/api/token/rotate", a.handleTokenRotate)
}

// ---------- auth ----------

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			v = v[:i]
		}
		return strings.TrimSpace(v)
	}
	return peerHost(r.RemoteAddr)
}

func readPasswordBody(r *http.Request) (string, error) {
	var body struct {
		Password string `json:"password"`
		Old      string `json:"old"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		return "", err
	}
	return body.Password, nil
}

func (a *App) handleAuthState(w http.ResponseWriter, r *http.Request) {
	f := a.admin.Load()
	cfg := a.getConfig()
	grace := int64(0)
	if left := setupGrace - time.Since(time.Unix(startedAt, 0)); left > 0 {
		grace = int64(left.Seconds())
	}
	authed := a.sessionOK(r) || a.tokenOK(r) || a.classifyPeer(r) == peerLocal ||
		(a.classifyPeer(r) == peerLAN && cfg.LanTrust && cfg.AllowLAN)
	writeJSON(w, map[string]any{
		"has_password": f.Password != "",
		"needs_setup":  f.Password == "",
		"setup_grace":  grace,
		"authed":       authed,
		"lan_trust":    cfg.LanTrust,
		"allow_lan":    cfg.AllowLAN,
	})
}

func (a *App) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	ip := clientIP(r)
	if a.limiter.blocked(ip) {
		writeErr(w, 429, "尝试次数过多，请 5 分钟后再试")
		return
	}
	pw, err := readPasswordBody(r)
	if err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	f := a.admin.Load()
	if f.Password == "" {
		writeErr(w, 400, "尚未设置管理密码")
		return
	}
	if !verifyPassword(pw, f.Password) {
		a.limiter.fail(ip)
		log.Printf("login failed from %s", ip)
		writeErr(w, 401, "密码不正确")
		return
	}
	a.limiter.reset(ip)
	a.setSessionCookie(w)
	log.Printf("login ok from %s", ip)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if f := a.admin.Load(); f.Password != "" {
		writeErr(w, 403, "管理密码已设置，请直接登录")
		return
	}
	if !a.setupAllowed(r) {
		writeErr(w, 403, "已超出界面设置时限，请在服务器执行 -set-pass 命令")
		return
	}
	pw, err := readPasswordBody(r)
	if err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if len([]rune(pw)) < 6 {
		writeErr(w, 400, "密码至少 6 位")
		return
	}
	if err := a.admin.SetPassword(pw); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("admin password initialized from %s", clientIP(r))
	a.setSessionCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	f := a.admin.Load()
	if f.Password != "" && !verifyPassword(body.Old, f.Password) {
		writeErr(w, 401, "原密码不正确")
		return
	}
	if len([]rune(body.New)) < 6 {
		writeErr(w, 400, "新密码至少 6 位")
		return
	}
	if err := a.admin.SetPassword(body.New); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.setSessionCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	if strings.TrimSpace(body.Token) != a.getConfig().APIToken {
		writeErr(w, 401, "token 不正确")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "lxcpanel_token",
		Value:    a.getConfig().APIToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 3600,
	})
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- system ----------

type SystemInfo struct {
	Hostname    string  `json:"hostname"`
	OSName      string  `json:"os_name"`
	Kernel      string  `json:"kernel"`
	Arch        string  `json:"arch"`
	UptimeSec   int64   `json:"uptime_sec"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	CPUCount    int     `json:"cpu_count"`
	MemTotal    uint64  `json:"mem_total"`
	MemAvail    uint64  `json:"mem_avail"`
	SwapTotal   uint64  `json:"swap_total"`
	SwapFree    uint64  `json:"swap_free"`
	DiskTotal   uint64  `json:"disk_total"`
	DiskFree    uint64  `json:"disk_free"`
	DiskPath    string  `json:"disk_path"`
	LXCVersion  string  `json:"lxc_version"`
	PanelVer    string  `json:"panel_version"`
	Port        int     `json:"port"`
	DataDir     string  `json:"data_dir"`
	StartedAt   int64   `json:"started_at"`
	ServerTime  int64   `json:"server_time"`
	CgroupVer   int     `json:"cgroup_version"`
	RootRunning bool    `json:"root_running"`
}

var startedAt = time.Now().Unix()

func readMeminfo() map[string]uint64 {
	out := map[string]uint64{}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(strings.TrimSpace(v))
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		out[k] = n * 1024
	}
	return out
}

func systemInfo(cfg Config, dataDir string) SystemInfo {
	host, _ := os.Hostname()
	si := SystemInfo{
		Hostname:   host,
		Arch:       runtime.GOARCH,
		CPUCount:   runtime.NumCPU(),
		PanelVer:   appVersion,
		Port:       cfg.Port,
		DataDir:    dataDir,
		StartedAt:  startedAt,
		ServerTime: time.Now().Unix(),
		DiskPath:   lxcPath(),
	}
	if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		si.Kernel = strings.TrimSpace(string(raw))
	}
	if raw, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok && k == "PRETTY_NAME" {
				si.OSName = strings.Trim(strings.TrimSpace(v), `"`)
			}
		}
	}
	if raw, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(raw))
		if len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				si.UptimeSec = int64(v)
			}
		}
	}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(raw))
		if len(f) >= 3 {
			si.Load1, _ = strconv.ParseFloat(f[0], 64)
			si.Load5, _ = strconv.ParseFloat(f[1], 64)
			si.Load15, _ = strconv.ParseFloat(f[2], 64)
		}
	}
	m := readMeminfo()
	si.MemTotal = m["MemTotal"]
	si.MemAvail = m["MemAvailable"]
	if si.MemAvail == 0 {
		si.MemAvail = m["MemFree"]
	}
	si.SwapTotal = m["SwapTotal"]
	si.SwapFree = m["SwapFree"]
	si.DiskTotal, si.DiskFree = diskUsage(lxcPath())
	if si.DiskTotal == 0 {
		si.DiskPath = "/"
		si.DiskTotal, si.DiskFree = diskUsage("/")
	}
	if haveCmd("lxc-info") {
		if out, err := runCmd(6*time.Second, "lxc-info", "--version"); err == nil {
			si.LXCVersion = strings.TrimSpace(out)
		}
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		si.CgroupVer = 2
	} else {
		si.CgroupVer = 1
	}
	si.RootRunning = os.Geteuid() == 0
	return si
}

func (a *App) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, systemInfo(a.getConfig(), a.dataDir))
}

func (a *App) handleEnv(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.Env())
}

func (a *App) handleEnvInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	env := a.Env()
	if env.Installed && env.HasDownload {
		writeErr(w, 400, "LXC 运行环境已就绪，无需安装")
		return
	}
	pm := env.PkgManager
	t := a.tasks.Submit("install", "安装 LXC 运行环境", func(logf func(string)) error {
		if pm == "" {
			logf(installCmd(""))
			return fmt.Errorf("未找到可用的包管理器")
		}
		err := runInstall(pm, logf)
		a.invalidateEnv()
		if err == nil {
			if e := a.Env(); e.Installed {
				logf("LXC 运行环境已就绪，版本 " + e.Version)
			} else {
				logf("安装命令已执行，但未检测到 lxc 命令，请检查上方日志")
			}
		}
		return err
	})
	writeJSON(w, map[string]string{"task_id": t.ID})
}

func (a *App) handleDistros(w http.ResponseWriter, r *http.Request) {
	env := a.Env()
	writeJSON(w, map[string]any{
		"distros":      distroCatalog(),
		"servers":      imageServers,
		"arch":         runtime.GOARCH,
		"has_download": env.HasDownload,
		"templates":    env.Templates,
	})
}

// ---------- overview ----------

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	containers := a.sampler.Snapshot()
	if containers == nil {
		containers = []*Container{}
	}
	points := a.sampler.HostHistory()
	if points == nil {
		points = []Point{}
	}
	cfg := a.getConfig()

	running, frozen, stopped := 0, 0, 0
	var memUsed uint64
	var cpuUsed float64
	for _, c := range containers {
		switch c.State {
		case "RUNNING":
			running++
			memUsed += c.MemBytes
			cpuUsed += c.CPUPercent
		case "FROZEN":
			frozen++
		default:
			stopped++
		}
	}
	writeJSON(w, map[string]any{
		"system":     systemInfo(cfg, a.dataDir),
		"env":        a.Env(),
		"containers": containers,
		"summary": map[string]int{
			"total": len(containers), "running": running, "frozen": frozen, "stopped": stopped,
		},
		"usage": map[string]any{
			"mem_used": memUsed, "cpu_used": round2(cpuUsed),
		},
		"host_points": points,
		"sample_secs": cfg.SampleSecs,
		"error":       a.sampler.LastError(),
	})
}

// ---------- containers ----------

func (a *App) handleContainers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list := a.sampler.Snapshot()
		if list == nil {
			list = []*Container{}
		}
		writeJSON(w, map[string]any{"containers": list, "error": a.sampler.LastError()})
	case http.MethodPost:
		if rejectIfStorageBusy(w) {
			return
		}
		var req CreateRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			writeErr(w, 400, "请求格式错误")
			return
		}
		if err := sanitizeCreate(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if env := a.Env(); !env.Installed || !env.HasDownload {
			writeErr(w, 400, "LXC 运行环境不完整，请先在「环境」页完成安装")
			return
		}
		if req.Server == "" {
			req.Server = a.getConfig().ImageHost
		}
		name := req.Name
		// 创建时就随机生成 root 密码，容器里生效并记进 meta，列表里直接可见
		rootPass := randomPassword(defaultPassLen)
		t := a.tasks.Submit("create", "创建容器 "+name, func(logf func(string)) error {
			err := runCreate(req, logf)
			if err != nil {
				logf("失败：" + err.Error())
				return err
			}
			now := time.Now().Unix()
			a.sampler.meta.Patch(name, func(m *ContainerMeta) {
				m.Distro, m.Release, m.Arch = req.Distro, req.Release, req.Arch
				m.CreatedAt = now
				m.RootPass, m.RootPassAt = rootPass, now
			})
			logf("正在设置 root 密码 ...")
			if w, err := setRootPassword(name, rootPass, logf); err != nil {
				logf("警告：root 密码没能写入容器（" + err.Error() + "），密码已记录，可稍后在容器详情里重新设置")
			} else if w != nil && !w.Verified {
				logf("警告：" + w.Detail)
			}
			a.applyProxy(name, logf)
			logf("完成：容器 " + name + " 已创建，root 密码 " + rootPass)
			a.sampler.Snapshot()
			return nil
		})
		writeJSON(w, map[string]string{"task_id": t.ID, "root_pass": rootPass})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (a *App) handleContainerSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/containers/"), "/")
	if rest == "" {
		writeErr(w, 404, "not found")
		return
	}
	parts := strings.Split(rest, "/")
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "非法容器名")
		return
	}
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	switch sub {
	case "":
		switch r.Method {
		case http.MethodGet:
			c, err := findContainer(name)
			if err != nil {
				writeErr(w, 404, err.Error())
				return
			}
			// os-release 读不到时（容器已停止、loop 镜像未挂载）用创建时记下的信息兜底
			if m, ok := a.sampler.meta.Get(name); ok {
				if c.Distro == "" {
					c.Distro, c.Release = m.Distro, m.Release
				}
				if c.OS == "" {
					c.OS = m.OS
				}
				c.RootPass = m.RootPass
			}
			if c.OS == "" {
				c.OS = distroLabel(c.Distro, c.Release)
			}
			c.CPUPercent = 0
			if pts := a.sampler.History(name); len(pts) > 0 {
				c.CPUPercent = pts[len(pts)-1].CPU
			}
			writeJSON(w, c)
		case http.MethodDelete:
			if rejectIfStorageBusy(w) {
				return
			}
			if _, err := os.Stat(filepath.Join(lxcPath(), name)); err != nil {
				writeErr(w, 404, "容器不存在")
				return
			}
			t := a.tasks.Submit("destroy", "删除容器 "+name, func(logf func(string)) error {
				if out, err := containerAction(name, "stop"); err != nil {
					logf("停止容器（可能已停止）: " + strings.TrimSpace(out))
				}
				if err := runStream(logf, "lxc-destroy", append(lxcPathArgs(), "-n", name, "-f")...); err != nil {
					return err
				}
				a.sampler.meta.Delete(name)
				logf("容器 " + name + " 已删除")
				a.sampler.Snapshot()
				return nil
			})
			writeJSON(w, map[string]string{"task_id": t.ID})
		default:
			writeErr(w, 405, "method not allowed")
		}
	case "action":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method not allowed")
			return
		}
		if rejectIfStorageBusy(w) {
			return
		}
		var body struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "请求格式错误")
			return
		}
		if _, err := os.Stat(filepath.Join(lxcPath(), name)); err != nil {
			writeErr(w, 404, "容器不存在")
			return
		}
		// 启动/重启前先把宿主机侧网络补齐（桥接、转发、NAT、DHCP），
		// 否则容器起来了也没有 IP 或出不了网。
		if body.Action == "start" || body.Action == "restart" {
			hostNetFix(nil)
		}
		out, err := containerAction(name, body.Action)
		a.sampler.Snapshot()
		if err != nil {
			writeErr(w, 500, strings.TrimSpace(out))
			return
		}
		// 容器起来后 IP 才确定，端口转发要跟着刷新（停止时跳过也无害）
		if body.Action == "start" || body.Action == "restart" || body.Action == "unfreeze" {
			go func() {
				time.Sleep(2 * time.Second)
				a.syncPorts(nil)
				// 容器刚起来，rootfs 才挂上，这时才写得进代理配置
				a.applyProxy(name, nil)
			}()
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "logs":
		if r.Method != http.MethodGet {
			writeErr(w, 405, "method not allowed")
			return
		}
		lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		writeJSON(w, map[string]string{"log": containerLogs(name, lines)})
	case "config":
		if r.Method == http.MethodGet {
			raw, err := os.ReadFile(filepath.Join(lxcPath(), name, "config"))
			if err != nil {
				writeErr(w, 404, "读取配置失败")
				return
			}
			writeJSON(w, map[string]string{"config": string(raw)})
			return
		}
		writeErr(w, 405, "method not allowed")
	case "limits":
		if r.Method != http.MethodPut {
			writeErr(w, 405, "method not allowed")
			return
		}
		var body struct {
			MemoryMB  int     `json:"memory_mb"`
			CPUs      float64 `json:"cpus"`
			AutoStart bool    `json:"autostart"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "请求格式错误")
			return
		}
		info, _ := lxcInfo(name)
		if body.MemoryMB > 0 {
			if err := setConfigKey(name, "lxc.cgroup2.memory.max", strconv.FormatInt(int64(body.MemoryMB)*1024*1024, 10)); err != nil {
				writeErr(w, 500, "写入配置失败: "+err.Error())
				return
			}
			if info != nil && info.State == "RUNNING" {
				runCmd(10*time.Second, "lxc-cgroup", append(lxcPathArgs(), "-n", name, "memory.max", strconv.FormatInt(int64(body.MemoryMB)*1024*1024, 10))...)
			}
		} else {
			removeConfigKey(name, "lxc.cgroup2.memory.max")
		}
		if body.CPUs > 0 {
			val := fmt.Sprintf("%d 100000", int(body.CPUs*100000))
			if err := setConfigKey(name, "lxc.cgroup2.cpu.max", val); err != nil {
				writeErr(w, 500, "写入配置失败: "+err.Error())
				return
			}
			if info != nil && info.State == "RUNNING" {
				runCmd(10*time.Second, "lxc-cgroup", append(lxcPathArgs(), "-n", name, "cpu.max", val)...)
			}
		} else {
			removeConfigKey(name, "lxc.cgroup2.cpu.max")
		}
		if body.AutoStart {
			setConfigKey(name, "lxc.start.auto", "1")
		} else {
			removeConfigKey(name, "lxc.start.auto")
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "stats":
		if r.Method != http.MethodGet {
			writeErr(w, 405, "method not allowed")
			return
		}
		writeJSON(w, map[string]any{"points": a.sampler.History(name)})
	case "ipv6":
		a.handleContainerIPv6(w, r, name)
	case "password":
		a.handleContainerPassword(w, r, name)
	case "login":
		a.handleContainerLogin(w, r, name)
	case "netdiag":
		if _, err := os.Stat(filepath.Join(lxcPath(), name)); err != nil {
			writeErr(w, 404, "容器不存在")
			return
		}
		probes, err := probeContainerNet(name, a.proxyURL())
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]any{"probes": probes})
	case "netfix":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method not allowed")
			return
		}
		if _, err := os.Stat(filepath.Join(lxcPath(), name)); err != nil {
			writeErr(w, 404, "容器不存在")
			return
		}
		logs, err := hostNetFix(nil)
		if err != nil {
			writeJSON(w, map[string]any{"logs": logs, "error": err.Error()})
			return
		}
		a.applyProxy(name, func(s string) { logs = append(logs, s) })
		st := hostNetStatus()
		st.Proxy = a.proxyURL()
		writeJSON(w, map[string]any{"logs": logs, "status": st})
	case "diag":
		if r.Method != http.MethodGet {
			writeErr(w, 405, "method not allowed")
			return
		}
		if _, err := os.Stat(filepath.Join(lxcPath(), name)); err != nil {
			writeErr(w, 404, "容器不存在")
			return
		}
		writeJSON(w, map[string]string{"text": containerDiagText(name)})
	case "disk":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method not allowed")
			return
		}
		if rejectIfStorageBusy(w) {
			return
		}
		var body struct {
			SizeGB int `json:"size_gb"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
			writeErr(w, 400, "请求格式错误")
			return
		}
		if body.SizeGB < 0 || body.SizeGB > 1024 {
			writeErr(w, 400, "容量需在 0 - 1024 GB 之间（0 表示不限制）")
			return
		}
		if _, err := os.Stat(filepath.Join(lxcPath(), name, "config")); err != nil {
			writeErr(w, 404, "容器不存在")
			return
		}
		t := a.tasks.Submit("disk", "调整 "+name+" 的磁盘容量", func(logf func(string)) error {
			storageBusy.Store(true)
			defer storageBusy.Store(false)
			if err := resizeContainerDisk(name, body.SizeGB, logf); err != nil {
				logf("失败：" + err.Error())
				return err
			}
			a.sampler.Snapshot()
			return nil
		})
		writeJSON(w, map[string]string{"task_id": t.ID})
	case "terminal":
		a.handleTerminalCreate(w, r, name)
	default:
		writeErr(w, 404, "not found")
	}
}

// ---------- tasks ----------

func (a *App) handleTasksList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleTaskRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	t := a.tasks.Get(id)
	if t == nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	if len(parts) == 1 {
		writeJSON(w, t.Snapshot())
		return
	}
	if parts[1] != "stream" {
		writeErr(w, 404, "not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")

	sent := 0
	send := func() bool {
		t.mu.Lock()
		lines := make([]string, len(t.Output))
		copy(lines, t.Output)
		done := t.done
		status := t.Status
		errMsg := t.Error
		t.mu.Unlock()
		for ; sent < len(lines); sent++ {
			b, _ := json.Marshal(map[string]string{"line": lines[sent]})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if done {
			b, _ := json.Marshal(map[string]string{"status": status, "error": errMsg})
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", b)
		}
		flusher.Flush()
		return done
	}
	if send() {
		return
	}

	_, ch, done := t.Follow()
	if done {
		send()
		return
	}
	defer t.Unfollow(ch)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if send() {
				return
			}
		case _, ok := <-ch:
			if send() {
				return
			}
			if !ok {
				return
			}
		}
	}
}

// ---------- term routing ----------

func (a *App) handleTermRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/term/"), "/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	if len(parts) == 1 {
		if r.Method == http.MethodDelete {
			a.handleTermClose(w, r, id)
			return
		}
		writeErr(w, 404, "not found")
		return
	}
	switch parts[1] {
	case "stream":
		a.handleTermStream(w, r, id)
	case "input":
		a.handleTermInput(w, r, id)
	case "resize":
		a.handleTermResize(w, r, id)
	default:
		writeErr(w, 404, "not found")
	}
}

// ---------- settings ----------

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := a.getConfig()
		writeJSON(w, map[string]any{
			"port":         cfg.Port,
			"allow_lan":    cfg.AllowLAN,
			"lan_trust":    cfg.LanTrust,
			"has_password": a.admin.Load().Password != "",
			"image_host":   cfg.ImageHost,
			"sample_secs":  cfg.SampleSecs,
			"proxy_url":    cfg.ProxyURL,
			"api_token":    cfg.APIToken,
			"token_file":   a.tokenFilePath(),
			"data_dir":     a.dataDir,
			"version":      appVersion,
			"servers":      imageServers,
			"storage":      currentStorage(),
			"cache":        currentCache(),
		})
	case http.MethodPut:
		var body struct {
			AllowLAN   *bool   `json:"allow_lan"`
			LanTrust   *bool   `json:"lan_trust"`
			ImageHost  *string `json:"image_host"`
			SampleSecs *int    `json:"sample_secs"`
			ProxyURL   *string `json:"proxy_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "请求格式错误")
			return
		}
		a.mu.Lock()
		if body.AllowLAN != nil {
			a.cfg.AllowLAN = *body.AllowLAN
		}
		if body.LanTrust != nil {
			a.cfg.LanTrust = *body.LanTrust
		}
		if body.ImageHost != nil && *body.ImageHost != "" {
			a.cfg.ImageHost = *body.ImageHost
		}
		if body.SampleSecs != nil && *body.SampleSecs >= 3 && *body.SampleSecs <= 120 {
			a.cfg.SampleSecs = *body.SampleSecs
		}
		if body.ProxyURL != nil {
			if p, e := normalizeProxy(*body.ProxyURL); e == nil {
				a.cfg.ProxyURL = p
			}
		}
		a.mu.Unlock()
		if err := a.saveConfig(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (a *App) handleTokenRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	a.mu.Lock()
	a.cfg.APIToken = randomToken()
	tok := a.cfg.APIToken
	a.mu.Unlock()
	if err := a.saveConfig(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.writeTokenFile()
	http.SetCookie(w, &http.Cookie{
		Name: "lxcpanel_token", Value: tok, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600,
	})
	writeJSON(w, map[string]string{"api_token": tok})
}

// ---------- 容器 root 密码 ----------

// handleContainerPassword 修改容器 root 密码；传 random=true 或留空密码即重新随机生成。
func (a *App) handleContainerPassword(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if _, err := os.Stat(filepath.Join(lxcPath(), name)); err != nil {
		writeErr(w, 404, "容器不存在")
		return
	}
	var body struct {
		Password string `json:"password"`
		Random   bool   `json:"random"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)

	pass := strings.TrimSpace(body.Password)
	if body.Random || pass == "" {
		pass = randomPassword(defaultPassLen)
	} else if err := validPassword(pass); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// 容器没运行时这里会临时启动再停回，耗时可能几秒
	pw, err := setRootPassword(name, pass, nil)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.sampler.meta.Patch(name, func(m *ContainerMeta) {
		m.RootPass = pass
		m.RootPassAt = time.Now().Unix()
	})
	a.sampler.Snapshot()

	// 顺便告诉前端「写进去了」和「能不能登录」是两件事
	login, _ := probeLogin(name)
	res := map[string]any{"password": pass, "write": pw}
	if login != nil {
		res["login"] = login
	}
	writeJSON(w, res)
}

// handleContainerLogin 检查 / 修复容器的 SSH 登录条件。
//
// GET  → 只检查，返回 LoginStatus；
// POST → 修复，body: {"install": true} 才会安装 openssh-server（走任务，耗时长）。
func (a *App) handleContainerLogin(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := os.Stat(filepath.Join(lxcPath(), name)); err != nil {
		writeErr(w, 404, "容器不存在")
		return
	}
	switch r.Method {
	case http.MethodGet:
		st, err := probeLogin(name)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, st)

	case http.MethodPost:
		var body struct {
			Install bool `json:"install"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&body)

		pass := ""
		if m, ok := a.sampler.meta.Get(name); ok {
			pass = m.RootPass
		}
		if !body.Install {
			// 不装包的部分都是秒级的，直接同步返回
			res, err := fixLogin(name, pass, false, nil)
			if err != nil {
				writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "logs": res.Logs, "status": res.Status, "need_install": res.NeedInstall})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "logs": res.Logs, "status": res.Status, "need_install": res.NeedInstall})
			return
		}

		t := a.tasks.Submit("loginfix", "修复 "+name+" 的 SSH 登录", func(logf func(string)) error {
			res, err := fixLogin(name, pass, true, logf)
			if err != nil {
				logf("失败：" + err.Error())
				return err
			}
			if res.Status != nil && res.Status.Ready {
				logf("完成：现在可以用 root 密码 SSH 登录")
			}
			return nil
		})
		writeJSON(w, map[string]string{"task_id": t.ID})

	default:
		writeErr(w, 405, "method not allowed")
	}
}

// ---------- 宿主机网络 ----------

// handleHostNet 查看/修复容器出网依赖的宿主机网络（桥接、转发、NAT、DHCP）。
func (a *App) handleHostNet(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st := hostNetStatus()
		st.Proxy = a.proxyURL()
		writeJSON(w, st)
	case http.MethodPost:
		var body struct {
			Proxy *string `json:"proxy"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)

		logs, err := hostNetFix(nil)
		res := map[string]any{"logs": logs}

		// 带了 proxy 字段就顺便保存并下发到所有运行中的容器
		if body.Proxy != nil {
			p, e := normalizeProxy(*body.Proxy)
			if e != nil {
				res["error"] = e.Error()
			} else {
				a.mu.Lock()
				a.cfg.ProxyURL = p
				a.mu.Unlock()
				if e := a.saveConfig(); e != nil {
					logs = append(logs, "保存代理配置失败："+e.Error())
				} else if p == "" {
					applyProxyToRunning("", func(s string) { logs = append(logs, s) })
				} else {
					logs = append(logs, "容器代理已设为 "+p)
					applyProxyToRunning(p, func(s string) { logs = append(logs, s) })
				}
			}
		}
		if err != nil {
			res["error"] = err.Error()
		}
		st := hostNetStatus()
		st.Proxy = a.proxyURL()
		res["logs"] = logs
		res["status"] = st
		writeJSON(w, res)
	default:
		writeErr(w, 405, "method not allowed")
	}
}
