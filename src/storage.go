package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

/* ------------------------------------------------------------------
   容器存储位置

   LXC 默认把容器放在 /var/lib/lxc（系统根分区）。飞牛设备的系统盘
   常常只有几十 G，几个 rootfs 就能把它吃满，所以允许把容器改放到
   数据盘（例如 /vol1/lxc）。

   切换目录时如果目标位置不同，需要把已有容器一起搬过去。LXC 的
   config 里记的是 rootfs 的绝对路径（lxc.rootfs.path = dir:/...），
   光搬目录不改写配置，容器就起不来了，所以迁移分三步：
     1. 移动/复制容器目录到新路径
     2. 改写每个容器 config 里的 rootfs 绝对路径
     3. 删除旧位置的副本，最后落盘新配置
   任一步失败都会尽力回滚到迁移前的状态。
------------------------------------------------------------------ */

// storageOpMu 保证同一时间只有一个存储迁移任务在跑。
var storageOpMu sync.Mutex

// storageBusy 为 true 时拒绝启停/创建/删除容器，避免迁移过程中 rootfs 被同时读写。
var storageBusy atomic.Bool

// rejectIfStorageBusy 在迁移期间拦住会改容器状态的操作。
func rejectIfStorageBusy(w http.ResponseWriter) bool {
	if storageBusy.Load() {
		writeErr(w, 409, "正在调整容器存储位置，请等迁移结束再操作容器")
		return true
	}
	return false
}

// 这些目录属于系统关键位置，放容器会出事（重启清空、被系统更新覆盖等）。
var blockedStorageRoots = []string{
	"/proc", "/sys", "/dev", "/run", "/boot", "/etc", "/usr",
	"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/snap", "/tmp",
	"/var/log", "/var/cache", "/var/run",
}

// normalizeStorageDir 校验并规范化用户填写的容器目录。
// 空字符串代表恢复系统默认的 /var/lib/lxc。
func normalizeStorageDir(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return defaultLXCStateDir, nil
	}
	return normalizeDirPath(p, nil)
}

// normalizeCacheDir 校验镜像缓存目录。默认的 /var/cache/lxc 位于系统目录树，
// 因此单独放行它自己（但不放行 /var/cache 下的其它位置）。
func normalizeCacheDir(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return defaultLXCCacheDir, nil
	}
	return normalizeDirPath(p, []string{defaultLXCCacheDir})
}

// normalizeDirPath 是容器目录与缓存目录共用的校验器。
// allow 中的路径即使落在系统目录下也予放行（用于默认目录）。
func normalizeDirPath(p string, allow []string) (string, error) {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		return "", errors.New("请填写绝对路径，例如 /vol1/lxc")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return "", errors.New("路径不能包含空格或控制字符")
		}
		// LXC 用配置文件里的这一行定位 rootfs，引号/逗号/反斜杠会被解析器截断。
		if strings.ContainsRune("\"'`,$\\", r) {
			return "", errors.New("路径不能包含引号、逗号、反引号、$ 或反斜杠")
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errors.New("路径不能包含 ..")
		}
	}
	// 这里用 path（而不是 filepath）：LXC 只跑在 Linux 上，路径一律是斜杠语义，
	// 用 path 还能让这些校验在开发机（Windows）上得到同样的结果。
	c := path.Clean(p)
	if c == "/" {
		return "", errors.New("不能直接使用根目录")
	}
	if strings.Count(c, "/") < 2 {
		return "", errors.New("请使用至少两级目录，例如 /vol1/lxc")
	}
	if len(c) > 200 {
		return "", errors.New("路径过长")
	}
	for _, bad := range blockedStorageRoots {
		if c == bad || strings.HasPrefix(c, bad+"/") {
			if allowedPath(c, allow) {
				continue
			}
			return "", fmt.Errorf("%s 是系统目录，不能用作存储目录", bad)
		}
	}
	return c, nil
}

// allowedPath 判断 c 是否命中放行名单（精确命中或位于其下）。
func allowedPath(c string, allow []string) bool {
	for _, a := range allow {
		if c == a || strings.HasPrefix(c, a+"/") {
			return true
		}
	}
	return false
}

// containersIn 列出目录下真正的 LXC 容器（有 config 文件的子目录）。
// 运行态的 lxc.monitor.* / lxc.payload.* 等临时目录没有 config，会被排除。
func containersIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() || !validName(e.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "config")); err != nil {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func fmtSize(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// rootfsBackend 返回容器 config 里 lxc.rootfs.path 的存储后端。
// 返回空字符串表示 config 没有该行（LXC 会使用默认的 dir 布局）。
func rootfsBackend(dir, name string) (string, string) {
	raw, err := os.ReadFile(filepath.Join(dir, name, "config"))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok || strings.TrimSpace(k) != "lxc.rootfs.path" {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		backend, _, _ := strings.Cut(v, ":")
		return backend, v
	}
	return "", ""
}

// rewriteRootfsBase 把容器 config 里 rootfs 的绝对路径从 oldBase 改到 newBase。
// dir 指定从哪个容器目录读取配置（迁移后要读目标目录下的那份）。
// 同时覆盖 dir: 与 loop: 两种后端（loop 记录的是 <base>/<name>/rootdev）。
func rewriteRootfsBase(dir, name, oldBase, newBase string) (bool, error) {
	p := filepath.Join(dir, name, "config")
	raw, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	from := path.Join(oldBase, name)
	to := path.Join(newBase, name)
	s := string(raw)
	if !strings.Contains(s, from) {
		return false, nil
	}
	s = strings.ReplaceAll(s, from, to)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ---------- status ----------

type StorageStatus struct {
	Dir         string `json:"dir"`
	Default     string `json:"default"`
	Custom      bool   `json:"custom"`
	Free        uint64 `json:"free"`
	Total       uint64 `json:"total"`
	Containers  int    `json:"containers"`
	LegacyDir   string `json:"legacy_dir"`
	LegacyCount int    `json:"legacy_count"`
}

func currentStorage() StorageStatus {
	dir := lxcPath()
	total, free := diskUsage(dir)
	st := StorageStatus{
		Dir:        dir,
		Default:    defaultLXCStateDir,
		Custom:     dir != defaultLXCStateDir,
		Total:      total,
		Free:       free,
		Containers: len(containersIn(dir)),
	}
	// 已经改成自定义目录，但默认目录里还留着容器时提醒用户。
	if st.Custom {
		if n := len(containersIn(defaultLXCStateDir)); n > 0 {
			st.LegacyDir = defaultLXCStateDir
			st.LegacyCount = n
		}
	}
	return st
}

// ---------- switch ----------

type storageSwitchReq struct {
	Path    string `json:"path"`
	Migrate bool   `json:"migrate"`
	// Kind 为 "cache" 时切换的是镜像缓存目录，其余情况切换容器目录。
	Kind string `json:"kind"`
}

func (a *App) handleStorageSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body storageSwitchReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if body.Kind == "cache" {
		a.handleCacheSwitch(w, body)
		return
	}
	target, err := normalizeStorageDir(body.Path)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	old := lxcPath()
	if target == old {
		writeErr(w, 400, "目标目录与当前存储位置相同")
		return
	}
	if !storageOpMu.TryLock() {
		writeErr(w, 409, "已有存储迁移任务正在进行，请稍候")
		return
	}
	storageOpMu.Unlock()

	t := a.tasks.Submit("storage", "切换容器存储位置", func(logf func(string)) error {
		storageOpMu.Lock()
		defer storageOpMu.Unlock()
		// 排队期间别人可能已经改过，重新确认。
		if cur := lxcPath(); cur != old {
			old = cur
		}
		return a.switchStorage(old, target, body.Migrate, logf)
	})
	writeJSON(w, map[string]string{"task_id": t.ID})
}

func (a *App) switchStorage(old, target string, migrate bool, logf func(string)) error {
	storageBusy.Store(true)
	defer storageBusy.Store(false)

	logf("当前容器目录: " + old)
	logf("目标容器目录: " + target)

	names := containersIn(old)
	if len(names) == 0 {
		logf("当前目录下没有容器，直接切换")
		return a.applyStorageDir(target, logf)
	}
	logf(fmt.Sprintf("当前目录下共有 %d 个容器：%s", len(names), strings.Join(names, "、")))

	if !migrate {
		logf(fmt.Sprintf("已选择“仅切换路径”：%d 个容器仍留在 %s，切换后不会出现在面板中", len(names), old))
		return a.applyStorageDir(target, logf)
	}

	// 1. 目标目录可用性
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %v", err)
	}
	probe := filepath.Join(target, ".lxcpanel-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("目标目录不可写: %v", err)
	}
	os.Remove(probe)

	// 2. 前置检查：运行状态、同名冲突、存储后端
	// 只放行明确 STOPPED 的容器：状态读不出来时宁可拦住，也不要挪动运行中的 rootfs。
	var running []string
	for _, n := range names {
		c, err := lxcInfo(n)
		if err == nil && c.State == "STOPPED" {
			continue
		}
		state := "UNKNOWN"
		if err == nil && c.State != "" {
			state = c.State
		}
		running = append(running, n+" ("+state+")")
	}
	if len(running) > 0 {
		logf("仍在运行的容器: " + strings.Join(running, "、"))
		return fmt.Errorf("以下容器还没有停止，请先停止再迁移：%s", strings.Join(running, "、"))
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(target, n)); err == nil {
			return fmt.Errorf("目标目录已存在同名容器 %s，请先清理后再迁移", n)
		}
		backend, val := rootfsBackend(old, n)
		if backend != "" && backend != "dir" && backend != "loop" {
			logf(fmt.Sprintf("容器 %s 使用 %s 存储后端（%s）", n, backend, val))
			return fmt.Errorf("容器 %s 不是目录/loop 存储（%s），暂不支持自动迁移", n, backend)
		}
	}

	// 3. 空间检查
	logf("正在统计容器占用体积…")
	var need int64
	for _, n := range names {
		need += dirSize(filepath.Join(old, n))
	}
	_, free := diskUsage(target)
	logf(fmt.Sprintf("待迁移数据约 %s，目标分区可用 %s", fmtSize(need), fmtSize(int64(free))))
	if need > 0 && uint64(float64(need)*1.05) > free {
		return fmt.Errorf("目标分区空间不足，至少需要 %s", fmtSize(int64(float64(need)*1.05)))
	}

	// 4. 搬运
	type moveRec struct{ name, how string }
	var done []moveRec
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			m := done[i]
			src := filepath.Join(old, m.name)
			dst := filepath.Join(target, m.name)
			if m.how == "rename" {
				if err := os.Rename(dst, src); err != nil {
					logf("回滚 " + m.name + " 失败: " + err.Error())
				}
			} else if err := os.RemoveAll(dst); err != nil {
				logf("清理 " + dst + " 失败: " + err.Error())
			}
		}
	}

	for i, n := range names {
		src := filepath.Join(old, n)
		dst := filepath.Join(target, n)
		logf(fmt.Sprintf("[%d/%d] 搬运 %s ...", i+1, len(names), n))
		if err := os.Rename(src, dst); err == nil {
			done = append(done, moveRec{n, "rename"})
			logf("    已移动（同一分区，秒级完成）")
			continue
		}
		// 跨分区：cp -a 保留权限/属主/硬链接，复制成功后再删源。
		logf("    跨分区，开始复制（可能耗时较久）...")
		if err := runStream(logf, "cp", "-a", src, dst); err != nil {
			os.RemoveAll(dst)
			rollback()
			return fmt.Errorf("复制容器 %s 失败: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(dst, "config")); err != nil {
			os.RemoveAll(dst)
			rollback()
			return fmt.Errorf("复制容器 %s 后没有找到 config，已中止", n)
		}
		done = append(done, moveRec{n, "copy"})
		logf("    复制完成")
	}

	// 5. 改写 rootfs 绝对路径（config 在 target 下）
	rewritten := []string{}
	for _, m := range done {
		ok, err := rewriteRootfsBase(target, m.name, old, target)
		if err != nil {
			for _, n := range rewritten {
				if _, e := rewriteRootfsBase(target, n, target, old); e != nil {
					logf("还原 " + n + " 的 rootfs 路径失败: " + e.Error())
				}
			}
			rollback()
			return fmt.Errorf("改写容器 %s 的配置失败: %v", m.name, err)
		}
		if ok {
			rewritten = append(rewritten, m.name)
		}
	}
	if len(rewritten) > 0 {
		logf(fmt.Sprintf("已更新 %d 个容器的 rootfs 路径", len(rewritten)))
	}

	// 6. 清理旧副本（复制模式才需要）
	for _, m := range done {
		if m.how != "copy" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(old, m.name)); err != nil {
			logf("清理旧副本 " + m.name + " 失败: " + err.Error())
		}
	}

	// 7. 落盘配置并让面板按新路径工作
	return a.applyStorageDir(target, logf)
}

// lxcGlobalConf 是 LXC 的全局配置文件。把 lxc.lxcpath 写在这里，
// 系统启动时的 lxc-autostart 才会去新目录里找容器，否则「开机自启」会静默失效。
const lxcGlobalConf = "/etc/lxc/lxc.conf"

// syncLXCConf 让系统级 lxc.lxcpath 与面板当前设置保持一致。
// 失败只记录日志，不影响面板自身的容器操作（那些都显式带了 --lxcpath）。
func syncLXCConf(dir string, logf func(string)) {
	raw, err := os.ReadFile(lxcGlobalConf)
	if err != nil && !os.IsNotExist(err) {
		logf("读取 " + lxcGlobalConf + " 失败: " + err.Error())
		return
	}
	var keep []string
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			keep = append(keep, line)
			continue
		}
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == "lxc.lxcpath" {
			continue
		}
		keep = append(keep, line)
	}
	// 去掉首尾空行
	for len(keep) > 0 && strings.TrimSpace(keep[0]) == "" {
		keep = keep[1:]
	}
	for len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
		keep = keep[:len(keep)-1]
	}
	if dir != defaultLXCStateDir {
		keep = append(keep, "lxc.lxcpath = "+dir)
	}
	out := strings.Join(keep, "\n")
	if out != "" {
		out += "\n"
	}
	if out == string(raw) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(lxcGlobalConf), 0o755); err != nil {
		logf("创建 " + filepath.Dir(lxcGlobalConf) + " 失败: " + err.Error())
		return
	}
	if err := os.WriteFile(lxcGlobalConf, []byte(out), 0o644); err != nil {
		logf("写入 " + lxcGlobalConf + " 失败: " + err.Error())
		return
	}
	if dir == defaultLXCStateDir {
		logf("已从 " + lxcGlobalConf + " 移除 lxc.lxcpath（回到系统默认）")
	} else {
		logf("已同步 lxc.lxcpath = " + dir + " 到 " + lxcGlobalConf + "（系统开机自启同样生效）")
	}
}

func (a *App) applyStorageDir(dir string, logf func(string)) error {
	// 目标目录不存在时先建好，否则 lxc-ls 会直接报错。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %v", dir, err)
	}
	a.mu.Lock()
	if dir == defaultLXCStateDir {
		a.cfg.StorageDir = ""
	} else {
		a.cfg.StorageDir = dir
	}
	a.mu.Unlock()
	if err := a.saveConfig(); err != nil {
		return fmt.Errorf("保存配置失败: %v", err)
	}
	setLXCPath(dir)
	syncLXCConf(dir, logf)
	a.invalidateEnv()
	if a.sampler != nil {
		a.sampler.Invalidate()
		a.sampler.Snapshot()
	}
	logf("存储位置已切换为 " + dir)
	logf(fmt.Sprintf("该目录下现有 %d 个容器", len(containersIn(dir))))
	return nil
}

/* ------------------------------------------------------------------
   镜像缓存位置

   lxc-create -t download 会把下载好的 rootfs/meta 压缩包缓存在
   /var/cache/lxc 下（模板内部路径为 <base>/download/<distro>/...），
   换发行版或重建容器时可以复用，省一次下载。这个目录同样会吃掉
   系统盘空间，所以允许改到数据盘。

   改缓存目录不需要动容器配置，只是后续 lxc-create 会带不同的
   LXC_CACHE_PATH 环境变量；已有缓存可以一并搬过去。
------------------------------------------------------------------ */

type CacheStatus struct {
	Dir       string `json:"dir"`
	Default   string `json:"default"`
	Custom    bool   `json:"custom"`
	Free      uint64 `json:"free"`
	Total     uint64 `json:"total"`
	Bytes     int64  `json:"bytes"`
	Entries   int    `json:"entries"`
	LegacyDir string `json:"legacy_dir"`
	Legacy    int    `json:"legacy_count"`
}

// usageOf 返回目录所在分区用量；目录还不存在时向上找最近的已存在父目录。
func usageOf(dir string) (total, free uint64) {
	p := dir
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(p); err == nil {
			return diskUsage(p)
		}
		np := path.Dir(p)
		if np == p {
			break
		}
		p = np
	}
	return diskUsage("/")
}

func currentCache() CacheStatus {
	dir := lxcCacheDir()
	total, free := usageOf(dir)
	st := CacheStatus{
		Dir:     dir,
		Default: defaultLXCCacheDir,
		Custom:  dir != defaultLXCCacheDir,
		Total:   total,
		Free:    free,
		Bytes:   dirSize(dir),
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".lxcpanel") {
				continue
			}
			st.Entries++
		}
	}
	if st.Custom {
		if entries, err := os.ReadDir(defaultLXCCacheDir); err == nil {
			n := 0
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), ".lxcpanel") {
					n++
				}
			}
			if n > 0 {
				st.LegacyDir = defaultLXCCacheDir
				st.Legacy = n
			}
		}
	}
	return st
}

func (a *App) handleCacheSwitch(w http.ResponseWriter, body storageSwitchReq) {
	target, err := normalizeCacheDir(body.Path)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	old := lxcCacheDir()
	if target == old {
		writeErr(w, 400, "目标目录与当前缓存位置相同")
		return
	}
	if !storageOpMu.TryLock() {
		writeErr(w, 409, "已有存储任务正在进行，请稍候")
		return
	}
	storageOpMu.Unlock()

	t := a.tasks.Submit("storage", "切换模板缓存位置", func(logf func(string)) error {
		storageOpMu.Lock()
		defer storageOpMu.Unlock()
		if cur := lxcCacheDir(); cur != old {
			old = cur
		}
		return a.switchCacheDir(old, target, body.Migrate, logf)
	})
	writeJSON(w, map[string]string{"task_id": t.ID})
}

func (a *App) switchCacheDir(old, target string, migrate bool, logf func(string)) error {
	storageBusy.Store(true)
	defer storageBusy.Store(false)

	logf("当前缓存目录: " + old)
	logf("目标缓存目录: " + target)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %v", err)
	}
	probe := filepath.Join(target, ".lxcpanel-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("目标目录不可写: %v", err)
	}
	os.Remove(probe)

	entries, _ := os.ReadDir(old)
	if len(entries) == 0 {
		logf("当前缓存目录为空（或不存在），直接切换")
		return a.applyCacheDir(target, logf)
	}
	if !migrate {
		logf(fmt.Sprintf("已选择“仅切换路径”：%s 下的缓存文件保持原地，新下载会写入新目录", old))
		return a.applyCacheDir(target, logf)
	}

	need := dirSize(old)
	_, free := usageOf(target)
	logf(fmt.Sprintf("待迁移缓存约 %s，目标分区可用 %s", fmtSize(need), fmtSize(int64(free))))
	if need > 0 && uint64(float64(need)*1.05) > free {
		return fmt.Errorf("目标分区空间不足，至少需要 %s", fmtSize(int64(float64(need)*1.05)))
	}

	moved := 0
	for i, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".lxcpanel") {
			continue
		}
		src := filepath.Join(old, name)
		dst := filepath.Join(target, name)
		if _, err := os.Stat(dst); err == nil {
			logf(fmt.Sprintf("[%d/%d] 目标已有 %s，跳过（保留目标现有缓存）", i+1, len(entries), name))
			continue
		}
		logf(fmt.Sprintf("[%d/%d] 迁移 %s ...", i+1, len(entries), name))
		if err := os.Rename(src, dst); err == nil {
			moved++
			logf("    已移动（同一分区）")
			continue
		}
		logf("    跨分区，开始复制 ...")
		if err := runStream(logf, "cp", "-a", src, dst); err != nil {
			return fmt.Errorf("复制 %s 失败: %v", name, err)
		}
		if err := os.RemoveAll(src); err != nil {
			logf("    清理旧缓存 " + src + " 失败: " + err.Error())
		}
		moved++
	}
	logf(fmt.Sprintf("已迁移 %d 项缓存", moved))
	return a.applyCacheDir(target, logf)
}

func (a *App) applyCacheDir(dir string, logf func(string)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %v", dir, err)
	}
	a.mu.Lock()
	if dir == defaultLXCCacheDir {
		a.cfg.CacheDir = ""
	} else {
		a.cfg.CacheDir = dir
	}
	a.mu.Unlock()
	if err := a.saveConfig(); err != nil {
		return fmt.Errorf("保存配置失败: %v", err)
	}
	setLXCCacheDir(dir)
	a.invalidateEnv()
	logf("镜像缓存位置已切换为 " + dir)
	logf("之后创建容器都会把镜像下载到该目录")
	return nil
}
