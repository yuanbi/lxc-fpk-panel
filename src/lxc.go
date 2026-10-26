package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// defaultLXCStateDir 是 LXC 的系统默认容器目录。用户可以在面板里改成
	// 数据盘（例如 /vol1/lxc），此时所有 lxc-* 命令都要显式带 --lxcpath。
	defaultLXCStateDir = "/var/lib/lxc"
	lxcLogDir          = "/var/log/lxc"
	lxcTemplate        = "/usr/share/lxc/templates"
	// defaultLXCCacheDir 是 lxc-download 模板的镜像缓存目录。模板实际写入
	// <缓存目录>/download/<发行版>/<版本>/<架构>/<variant>，可以通过
	// LXC_CACHE_PATH 环境变量改到数据盘。
	defaultLXCCacheDir = "/var/cache/lxc"
)

var (
	stateDirMu   sync.RWMutex
	stateDirPath = defaultLXCStateDir

	cacheDirMu   sync.RWMutex
	cacheDirPath = defaultLXCCacheDir
)

// lxcPath 返回当前生效的容器目录。
func lxcPath() string {
	stateDirMu.RLock()
	defer stateDirMu.RUnlock()
	return stateDirPath
}

// setLXCPath 切换容器目录（只改内存状态，持久化由调用方负责）。
func setLXCPath(p string) {
	if strings.TrimSpace(p) == "" {
		p = defaultLXCStateDir
	}
	stateDirMu.Lock()
	stateDirPath = p
	stateDirMu.Unlock()
}

// lxcPathArgs 在非默认目录时返回 --lxcpath 参数，默认目录下保持原命令不变，
// 以兼容个别老版本 lxc 工具不支持该选项的情况。
func lxcPathArgs() []string {
	p := lxcPath()
	if p == defaultLXCStateDir {
		return nil
	}
	return []string{"--lxcpath=" + p}
}

// lxcCacheDir 返回当前生效的镜像缓存目录。
func lxcCacheDir() string {
	cacheDirMu.RLock()
	defer cacheDirMu.RUnlock()
	return cacheDirPath
}

// setLXCCacheDir 切换镜像缓存目录（持久化由调用方负责）。
func setLXCCacheDir(p string) {
	if strings.TrimSpace(p) == "" {
		p = defaultLXCCacheDir
	}
	cacheDirMu.Lock()
	cacheDirPath = p
	cacheDirMu.Unlock()
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

func validName(n string) bool { return nameRe.MatchString(n) }

// ---------- meta store ----------

type ContainerMeta struct {
	Name      string `json:"name"`
	Distro    string `json:"distro"`
	Release   string `json:"release"`
	Arch      string `json:"arch"`
	CreatedAt int64  `json:"created_at"`
	// OS 缓存解析成功的系统名（如 Debian GNU/Linux 12 (bookworm)）。
	// rootfs 读不到时（loop 镜像未挂载、容器已停止）也能继续显示系统。
	OS string `json:"os,omitempty"`
	// RootPass 是面板为容器生成的 root 密码，创建时随机生成、可在面板里修改。
	RootPass string `json:"root_pass,omitempty"`
	// RootPassAt 记录密码最后一次设置的时间。
	RootPassAt int64 `json:"root_pass_at,omitempty"`
}

type MetaStore struct {
	mu   sync.RWMutex
	path string
	data map[string]ContainerMeta
}

func NewMetaStore(path string) *MetaStore {
	s := &MetaStore{path: path, data: map[string]ContainerMeta{}}
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, &s.data)
	}
	return s
}

func (s *MetaStore) Get(name string) (ContainerMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.data[name]
	return m, ok
}

func (s *MetaStore) Set(m ContainerMeta) {
	s.mu.Lock()
	s.data[m.Name] = m
	raw, _ := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	os.WriteFile(s.path, raw, 0o644)
}

// Patch 只改 meta 里的部分字段，其余（尤其 root 密码）原样保留。
func (s *MetaStore) Patch(name string, fn func(*ContainerMeta)) {
	if strings.TrimSpace(name) == "" {
		return
	}
	s.mu.Lock()
	m := s.data[name]
	if m.Name == "" {
		m.Name = name
	}
	fn(&m)
	s.data[name] = m
	raw, _ := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	os.WriteFile(s.path, raw, 0o644)
}

func (s *MetaStore) Delete(name string) {
	s.mu.Lock()
	delete(s.data, name)
	raw, _ := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	os.WriteFile(s.path, raw, 0o644)
}

// ---------- exec helpers ----------

func runCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil && out == "" {
		out = err.Error()
	}
	return out, err
}

func haveCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ---------- container model ----------

type Container struct {
	Name       string   `json:"name"`
	State      string   `json:"state"`
	PID        int      `json:"pid"`
	IPv4       []string `json:"ipv4"`
	IPv6       []string `json:"ipv6"`
	MemBytes   uint64   `json:"mem_bytes"`
	MemLimit   uint64   `json:"mem_limit"`
	CPUSeconds float64  `json:"cpu_seconds"`
	CPUPercent float64  `json:"cpu_percent"`
	CPULimit   float64  `json:"cpu_limit"`
	AutoStart  bool     `json:"autostart"`
	Arch       string   `json:"arch"`
	OS         string   `json:"os"`
	Distro     string   `json:"distro"`
	Release    string   `json:"release"`
	Rootfs     string   `json:"rootfs"`
	CreatedAt  int64    `json:"created_at"`
	TXBytes    uint64   `json:"tx_bytes"`
	RXBytes    uint64   `json:"rx_bytes"`
	Privileged bool     `json:"privileged"`
	// DiskMode 为 "loop"（镜像，容量受限）或 "dir"（目录，不限制）。
	DiskMode  string `json:"disk_mode"`
	DiskBytes int64  `json:"disk_bytes"`
	// 网络相关（来自容器 config，IPv4/IPv6 的实际地址见 IPv4/IPv6 字段）
	Bridge  string   `json:"bridge"`
	Veth    string   `json:"veth"`
	MAC     string   `json:"mac"`
	IPv6Cfg []string `json:"ipv6_cfg"`
	NetOff  bool     `json:"net_off"`
	// RootPass 是面板生成/托管的 root 密码（来自 meta，容器里没有这个概念）。
	RootPass string `json:"root_pass,omitempty"`
}

func splitIPs(v string) (v4, v6 []string) {
	v4, v6 = []string{}, []string{}
	for _, part := range strings.Split(v, ",") {
		p := strings.TrimSpace(part)
		if p == "" || p == "-" {
			continue
		}
		if strings.Contains(p, ":") {
			v6 = append(v6, p)
		} else {
			v4 = append(v4, p)
		}
	}
	return
}

func parseBytes(v string) uint64 {
	f := strings.Fields(v)
	if len(f) == 0 {
		return 0
	}
	num, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	unit := ""
	if len(f) > 1 {
		unit = strings.ToLower(f[1])
	}
	switch {
	case unit == "" || strings.HasPrefix(unit, "byte"):
		return uint64(num)
	case strings.HasPrefix(unit, "kib"), strings.HasPrefix(unit, "kb"):
		return uint64(num * 1024)
	case strings.HasPrefix(unit, "mib"), strings.HasPrefix(unit, "mb"):
		return uint64(num * 1024 * 1024)
	case strings.HasPrefix(unit, "gib"), strings.HasPrefix(unit, "gb"):
		return uint64(num * 1024 * 1024 * 1024)
	case strings.HasPrefix(unit, "tib"), strings.HasPrefix(unit, "tb"):
		return uint64(num * 1024 * 1024 * 1024 * 1024)
	}
	return uint64(num)
}

func parseSeconds(v string) float64 {
	f := strings.Fields(v)
	if len(f) == 0 {
		return 0
	}
	num, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	unit := ""
	if len(f) > 1 {
		unit = strings.ToLower(f[1])
	}
	switch {
	case strings.HasPrefix(unit, "nanosecond"):
		return num / 1e9
	case strings.HasPrefix(unit, "microsecond"):
		return num / 1e6
	case strings.HasPrefix(unit, "millisecond"):
		return num / 1e3
	case strings.HasPrefix(unit, "minute"):
		return num * 60
	case strings.HasPrefix(unit, "hour"):
		return num * 3600
	}
	return num
}

func lxcInfo(name string) (*Container, error) {
	out, err := runCmd(12*time.Second, "lxc-info", append(lxcPathArgs(), "-n", name, "-H")...)
	c := &Container{Name: name}
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, err
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch strings.ToLower(k) {
		case "state":
			c.State = strings.ToUpper(v)
		case "pid":
			c.PID, _ = strconv.Atoi(v)
		case "ip":
			v4, v6 := splitIPs(v)
			c.IPv4 = append(c.IPv4, v4...)
			c.IPv6 = append(c.IPv6, v6...)
		case "cpu use":
			c.CPUSeconds = parseSeconds(v)
		case "memory use":
			c.MemBytes = parseBytes(v)
		case "tx bytes":
			c.TXBytes = parseBytes(v)
		case "rx bytes":
			c.RXBytes = parseBytes(v)
		}
	}
	if c.State == "" {
		c.State = "STOPPED"
	}
	return c, nil
}

func lxcListNames() ([]string, error) {
	if !haveCmd("lxc-ls") {
		return nil, errors.New("lxc-ls not found")
	}
	out, err := runCmd(15*time.Second, "lxc-ls", append(lxcPathArgs(), "-1")...)
	if err != nil {
		return nil, fmt.Errorf("lxc-ls failed: %s", strings.TrimSpace(out))
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			names = append(names, l)
		}
	}
	sort.Strings(names)
	return names, nil
}

// readContainerConfig parses the pieces of /var/lib/lxc/<name>/config we care about.
func readContainerConfig(name string) (autostart bool, memLimit uint64, cpuLimit float64, rootfs, arch string, privileged bool) {
	raw, err := os.ReadFile(filepath.Join(lxcPath(), name, "config"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "lxc.start.auto":
			autostart = v == "1"
		case "lxc.cgroup2.memory.max", "lxc.cgroup.memory.limit_in_bytes":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil && n < 1<<60 {
				memLimit = n
			}
		case "lxc.cgroup2.cpu.max", "lxc.cgroup.cpu.cfs_quota_us":
			if k == "lxc.cgroup2.cpu.max" {
				f := strings.Fields(v)
				if len(f) == 2 {
					q, e1 := strconv.ParseFloat(f[0], 64)
					p, e2 := strconv.ParseFloat(f[1], 64)
					if e1 == nil && e2 == nil && p > 0 {
						cpuLimit = q / p
					}
				}
			}
		case "lxc.rootfs.path":
			rootfs = strings.TrimPrefix(v, "dir:")
		case "lxc.arch":
			if strings.Contains(v, "arm64") {
				arch = "arm64"
			} else {
				arch = "amd64"
			}
		case "lxc.apparmor.profile":
			privileged = v == "unconfined" || v == "lxc-container-default-cgns"
		}
	}
	if rootfs == "" {
		rootfs = filepath.Join(lxcPath(), name, "rootfs")
	}
	return
}

// rootfsCandidates 列出可能读到 /etc/os-release 的目录，按可靠性排序。
//
// config 里的 lxc.rootfs.path 可能带后端前缀：dir:/... 是真实目录，
// loop:/... 指向镜像文件，overlay:/upper:/lower、btrfs:/... 这类则完全不是目录。
// 所以除了把配置里的路径本身当候选，还要加上 <lxcpath>/<name>/rootfs——
// 容器运行时后端都会把根文件系统挂到这里（停止后 loop 容器为空目录）。
func rootfsCandidates(name, rootfs string) []string {
	out := make([]string, 0, 4)
	add := func(p string) {
		p = strings.TrimSpace(p)
		// 配置里写的永远是以 / 开头的 Linux 路径；开发机上（Windows）测试时
		// 借 IsAbs 兜住系统自身拼出来的挂载点路径。
		if p == "" || !(strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || filepath.IsAbs(p)) {
			return
		}
		p = filepath.Clean(p)
		for _, e := range out {
			if e == p {
				return
			}
		}
		out = append(out, p)
	}
	if strings.Contains(rootfs, ":") {
		// 逐段尝试（overlay 的上下层目录里也可能有 os-release）
		for _, seg := range strings.Split(rootfs, ":") {
			add(seg)
		}
	} else {
		add(rootfs)
	}
	add(filepath.Join(lxcPath(), name, "rootfs"))
	return out
}

func readOSRelease(rootfs string) (pretty, id, ver string) {
	raw, err := os.ReadFile(filepath.Join(rootfs, "etc", "os-release"))
	if err != nil {
		return
	}
	return parseOSRelease(string(raw))
}

func parseOSRelease(text string) (pretty, id, ver string) {
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch k {
		case "PRETTY_NAME":
			pretty = v
		case "ID":
			id = v
		case "VERSION_ID":
			ver = v
		}
	}
	return
}

// resolveOS 尽最大努力识别容器的系统版本：先读 rootfs 里的 os-release，
// 读不到再让运行中的容器自己 cat 一份（loop 镜像、特殊挂载都能覆盖）。
func resolveOS(name, rootfs string, running bool) (pretty, id, ver string) {
	for _, dir := range rootfsCandidates(name, rootfs) {
		p, i, v := readOSRelease(dir)
		if p != "" || i != "" {
			return p, i, v
		}
	}
	if running {
		out, err := runCmd(6*time.Second, "lxc-attach", append(lxcPathArgs(), "-n", name, "--", "cat", "/etc/os-release")...)
		if err == nil {
			if p, i, v := parseOSRelease(out); p != "" || i != "" {
				return p, i, v
			}
		}
	}
	return "", "", ""
}

// distroLabel 用创建时记录的信息拼一个可读系统名，os-release 完全读不到时兜底，
// 避免列表里只显示一个「—」。
func distroLabel(distro, release string) string {
	if distro == "" {
		return ""
	}
	name := strings.ToUpper(distro[:1]) + distro[1:]
	for _, d := range distroCatalog() {
		if d.ID != distro {
			continue
		}
		name = d.Name
		for _, r := range d.Releases {
			if r.ID == release {
				return name + " " + r.Label
			}
		}
	}
	if release != "" {
		return name + " " + release
	}
	return name
}

// collect gathers a full snale state of all containers.
func collectContainers(meta *MetaStore) ([]*Container, error) {
	names, err := lxcListNames()
	if err != nil {
		return nil, err
	}
	out := make([]*Container, len(names))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, err := lxcInfo(n)
			if err != nil {
				c = &Container{Name: n, State: "UNKNOWN"}
			}
			// lxc-info 的 CPU / Memory 两行依赖它自己找 cgroup，个别系统上会是 0。
			// 顺着容器 init 进程的 /proc/<pid>/cgroup 直读更准，运行中以它为准。
			if c.State == "RUNNING" && c.PID > 0 {
				if v, ok := cgroupMemory(c.PID); ok {
					c.MemBytes = v
				}
				if v, ok := cgroupCPUSeconds(c.PID); ok {
					c.CPUSeconds = v
				}
			}
			as, mem, cpu, rootfs, arch, priv := readContainerConfig(n)
			c.AutoStart, c.MemLimit, c.CPULimit = as, mem, cpu
			c.Rootfs, c.Arch, c.Privileged = rootfs, arch, priv
			c.DiskMode, c.DiskBytes = containerDisk(n)
			nc := readNetConfig(n)
			c.Bridge, c.Veth, c.MAC, c.IPv6Cfg, c.NetOff = nc.Bridge, nc.Veth, nc.MAC, nc.IPv6Cfg, nc.Disabled
			if st, err := os.Stat(filepath.Join(lxcPath(), n)); err == nil {
				c.CreatedAt = st.ModTime().Unix()
			}
			var metaOS string
			if m, ok := meta.Get(n); ok {
				c.Distro, c.Release = m.Distro, m.Release
				metaOS = m.OS
				c.RootPass = m.RootPass
				if m.CreatedAt > 0 {
					c.CreatedAt = m.CreatedAt
				}
			}
			if c.Arch == "" {
				c.Arch = runtime.GOARCH
			}
			if c.OS == "" {
				pretty, id, ver := resolveOS(n, rootfs, c.State == "RUNNING")
				c.OS = pretty
				if c.OS == "" {
					c.OS = strings.Title(id) //nolint:staticcheck
				}
				if c.Distro == "" {
					c.Distro = id
				}
				if c.Release == "" {
					c.Release = ver
				}
				if c.OS != "" && metaOS != c.OS {
					// 缓存下来：容器停止后（尤其 loop 镜像未挂载）仍能显示系统。
					// 用 Patch 而不是 Set，避免把 root 密码等字段清空。
					distro, release, arch, createdAt, osName := c.Distro, c.Release, c.Arch, c.CreatedAt, c.OS
					meta.Patch(n, func(m *ContainerMeta) {
						m.Distro, m.Release, m.Arch = distro, release, arch
						m.CreatedAt, m.OS = createdAt, osName
					})
				}
			}
			if c.OS == "" && metaOS != "" {
				c.OS = metaOS
			}
			if c.OS == "" {
				c.OS = distroLabel(c.Distro, c.Release)
			}
			out[i] = c
		}(i, n)
	}
	wg.Wait()
	return out, nil
}

func findContainer(name string) (*Container, error) {
	if !validName(name) {
		return nil, errors.New("非法容器名")
	}
	if _, err := os.Stat(filepath.Join(lxcPath(), name)); err != nil {
		return nil, errors.New("容器不存在")
	}
	c, err := lxcInfo(name)
	if err != nil {
		c = &Container{Name: name, State: "UNKNOWN"}
	}
	as, mem, cpu, rootfs, arch, priv := readContainerConfig(name)
	c.AutoStart, c.MemLimit, c.CPULimit = as, mem, cpu
	c.Rootfs, c.Arch, c.Privileged = rootfs, arch, priv
	c.DiskMode, c.DiskBytes = containerDisk(name)
	nc := readNetConfig(name)
	c.Bridge, c.Veth, c.MAC, c.IPv6Cfg, c.NetOff = nc.Bridge, nc.Veth, nc.MAC, nc.IPv6Cfg, nc.Disabled
	if c.State == "RUNNING" && c.PID > 0 {
		if v, ok := cgroupMemory(c.PID); ok {
			c.MemBytes = v
		}
		if v, ok := cgroupCPUSeconds(c.PID); ok {
			c.CPUSeconds = v
		}
	}
	pretty, id, ver := resolveOS(name, rootfs, c.State == "RUNNING")
	if pretty == "" {
		pretty = strings.Title(id) //nolint:staticcheck
	}
	c.OS, c.Distro, c.Release = pretty, id, ver
	if st, err := os.Stat(filepath.Join(lxcPath(), name)); err == nil {
		c.CreatedAt = st.ModTime().Unix()
	}
	return c, nil
}

// ---------- config editing ----------

func setConfigKey(name, key, value string) error {
	path := filepath.Join(lxcPath(), name, "config")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	found := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key {
			lines[i] = fmt.Sprintf("%s = %s", key, value)
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, fmt.Sprintf("%s = %s", key, value))
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

func removeConfigKey(name, key string) error {
	path := filepath.Join(lxcPath(), name, "config")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var keep []string
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key && !strings.HasPrefix(t, "#") {
			continue
		}
		keep = append(keep, line)
	}
	return os.WriteFile(path, []byte(strings.Join(keep, "\n")), 0644)
}

// ---------- actions ----------

func containerAction(name, action string) (string, error) {
	if !validName(name) {
		return "", errors.New("非法容器名")
	}
	switch action {
	case "start":
		return runCmd(90*time.Second, "lxc-start", append(lxcPathArgs(), "-n", name, "-d")...)
	case "stop":
		return runCmd(90*time.Second, "lxc-stop", append(lxcPathArgs(), "-n", name, "-t", "30")...)
	case "kill":
		return runCmd(30*time.Second, "lxc-stop", append(lxcPathArgs(), "-n", name, "-k")...)
	case "restart":
		if out, err := runCmd(90*time.Second, "lxc-stop", append(lxcPathArgs(), "-n", name, "-t", "20")...); err != nil {
			return out, err
		}
		return runCmd(90*time.Second, "lxc-start", append(lxcPathArgs(), "-n", name, "-d")...)
	case "freeze":
		return runCmd(30*time.Second, "lxc-freeze", append(lxcPathArgs(), "-n", name)...)
	case "unfreeze":
		return runCmd(30*time.Second, "lxc-unfreeze", append(lxcPathArgs(), "-n", name)...)
	default:
		return "", errors.New("未知操作")
	}
}

func containerLogs(name string, lines int) string {
	if lines <= 0 || lines > 4000 {
		lines = 300
	}
	candidates := []string{
		filepath.Join(lxcLogDir, name+".log"),
	}
	var buf bytes.Buffer
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		all := strings.Split(string(raw), "\n")
		if len(all) > lines {
			all = all[len(all)-lines:]
		}
		buf.WriteString(strings.Join(all, "\n"))
		buf.WriteString("\n")
	}
	return buf.String()
}

// ---------- create ----------

type CreateRequest struct {
	Name       string  `json:"name"`
	Distro     string  `json:"distro"`
	Release    string  `json:"release"`
	Arch       string  `json:"arch"`
	MemoryMB   int     `json:"memory_mb"`
	CPUs       float64 `json:"cpus"`
	AutoStart  bool    `json:"autostart"`
	Server     string  `json:"server"`
	StartAfter bool    `json:"start_after"`
	// DiskGB 为 0 表示不限制磁盘（dir 存储，rootfs 直接落在宿主机分区上）；
	// 大于 0 时使用 loop 存储后端，创建一块 ext4 镜像并挂载为 rootfs。
	DiskGB int `json:"disk_gb"`
	// Network 为空或 "nat" 表示默认 veth 桥接出网；"none" 表示不配任何网卡。
	Network string `json:"network"`
	// IPv6 为 "on" 时创建后自动分配内网 ULA 地址（fd42:6c78:63::/64）。
	IPv6 string `json:"ipv6"`
}

func sanitizeCreate(r *CreateRequest) error {
	r.Name = strings.TrimSpace(r.Name)
	if !validName(r.Name) {
		return errors.New("容器名只能包含字母、数字、点、下划线、中划线，且不超过 63 字符")
	}
	if _, err := os.Stat(filepath.Join(lxcPath(), r.Name)); err == nil {
		return errors.New("同名容器已存在")
	}
	if r.Distro == "" {
		r.Distro = "debian"
	}
	if r.Release == "" {
		r.Release = "bookworm"
	}
	if r.Arch == "" {
		r.Arch = runtime.GOARCH
	}
	if r.MemoryMB < 0 || r.MemoryMB > 1024*1024 {
		return errors.New("内存参数超出范围")
	}
	if r.CPUs < 0 || r.CPUs > 256 {
		return errors.New("CPU 参数超出范围")
	}
	if r.DiskGB < 0 || r.DiskGB > 1024 {
		return errors.New("磁盘容量需在 0 - 1024 GB 之间（0 表示不限制）")
	}
	if r.DiskGB > 0 && !loopStorageOK() {
		return errors.New("当前系统缺少 loop 存储支持（/dev/loop-control、mount 或 mkfs.ext4），请选择“不限制磁盘”")
	}
	switch r.Network {
	case "", "nat", "none":
	default:
		return errors.New("网络模式只能是 nat 或 none")
	}
	switch r.IPv6 {
	case "", "off", "on":
	default:
		return errors.New("IPv6 选项只能是 on 或 off")
	}
	return nil
}

// runCreate performs the blocking lxc-create flow, streaming output to logf.
func runCreate(req CreateRequest, logf func(string)) error {
	args := append(lxcPathArgs(), "-n", req.Name, "-t", "download")
	if req.DiskGB > 0 {
		// -B loop 会在容器目录里建一块 ext4 镜像（rootdev），大小由 --fssize 决定。
		args = append(args, "-B", "loop", "--fstype", "ext4", "--fssize", fmt.Sprintf("%dG", req.DiskGB))
	}
	args = append(args, "--", "-d", req.Distro, "-r", req.Release, "-a", req.Arch)
	if req.Server != "" {
		// lxc-download 模板只接受长选项 --server（没有 -s 短选项）
		args = append(args, "--server", req.Server)
	}
	cacheDir := lxcCacheDir()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		logf("创建缓存目录 " + cacheDir + " 失败: " + err.Error())
	}
	logf("镜像缓存目录: " + cacheDir)
	if req.DiskGB > 0 {
		logf(fmt.Sprintf("磁盘容量: %d GB（loop 镜像，写入 %s/%s/rootdev）", req.DiskGB, lxcPath(), req.Name))
	} else {
		logf("磁盘容量: 不限制（dir 存储，与宿主机共用分区）")
	}
	logf("$ lxc-create " + strings.Join(args, " "))
	cmd := exec.Command("lxc-create", args...)
	cmd.Env = append(os.Environ(), "LXC_CACHE_PATH="+cacheDir, "DEBIAN_FRONTEND=noninteractive")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		logf(sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("lxc-create 失败: %v", err)
	}

	if req.MemoryMB > 0 {
		if err := setConfigKey(req.Name, "lxc.cgroup2.memory.max", strconv.FormatInt(int64(req.MemoryMB)*1024*1024, 10)); err != nil {
			logf("设置内存限制失败: " + err.Error())
		} else {
			logf(fmt.Sprintf("已设置内存上限: %d MiB", req.MemoryMB))
		}
	}
	if req.CPUs > 0 {
		quota := int(req.CPUs * 100000)
		if err := setConfigKey(req.Name, "lxc.cgroup2.cpu.max", fmt.Sprintf("%d 100000", quota)); err != nil {
			logf("设置 CPU 限制失败: " + err.Error())
		} else {
			logf(fmt.Sprintf("已设置 CPU 上限: %.2f 核", req.CPUs))
		}
	}
	if req.AutoStart {
		if err := setConfigKey(req.Name, "lxc.start.auto", "1"); err != nil {
			logf("设置开机自启失败: " + err.Error())
		} else {
			logf("已开启开机自启")
		}
	}
	applyNetworkOptions(req, logf)

	// 出网还依赖宿主机侧（桥接地址、ip_forward、NAT、DHCP），这里先补齐，
	// 否则容器起来了会「有网卡没 IP / 有 IP 出不去」。
	if req.Network != "none" {
		if _, err := hostNetFix(logf); err != nil {
			logf("宿主机网络修复未完全成功: " + err.Error())
		}
	}

	if req.StartAfter {
		logf("正在启动容器 ...")
		if out, err := containerAction(req.Name, "start"); err != nil {
			logf("启动失败: " + out)
		} else {
			logf("容器已启动")
		}
	}
	return nil
}

// ---------- environment ----------

type LXCEnv struct {
	Installed      bool     `json:"installed"`
	Version        string   `json:"version"`
	StateDir       string   `json:"state_dir"`
	StateWritable  bool     `json:"state_writable"`
	CacheDir       string   `json:"cache_dir"`
	CacheWritable  bool     `json:"cache_writable"`
	Templates      []string `json:"templates"`
	HasDownload    bool     `json:"has_download"`
	CgroupVersion  int      `json:"cgroup_version"`
	CgroupWritable bool     `json:"cgroup_writable"`
	PkgManager     string   `json:"pkg_manager"`
	Root           bool     `json:"root"`
	OSName         string   `json:"os_name"`
	Kernel         string   `json:"kernel"`
	Arch           string   `json:"arch"`
	Containers     int      `json:"containers"`
	Running        int      `json:"running"`
	Problems       []string `json:"problems"`
	Hints          []string `json:"hints"`
}

func detectEnv() LXCEnv {
	e := LXCEnv{
		StateDir:  lxcPath(),
		CacheDir:  lxcCacheDir(),
		Arch:      runtime.GOARCH,
		Root:      os.Geteuid() == 0,
		Templates: []string{},
		Problems:  []string{},
		Hints:     []string{},
	}
	bins := []string{"lxc-ls", "lxc-info", "lxc-create", "lxc-start", "lxc-stop", "lxc-attach"}
	missing := 0
	for _, b := range bins {
		if !haveCmd(b) {
			missing++
		}
	}
	e.Installed = missing == 0
	if haveCmd("lxc-info") {
		if out, err := runCmd(8*time.Second, "lxc-info", "--version"); err == nil {
			e.Version = strings.TrimSpace(out)
		}
	}

	// state dir
	if err := os.MkdirAll(lxcPath(), 0755); err == nil {
		probe := filepath.Join(lxcPath(), ".lxcpanel-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0644); err == nil {
			e.StateWritable = true
			os.Remove(probe)
		}
	}

	// cache dir（镜像下载缓存）
	if err := os.MkdirAll(lxcCacheDir(), 0755); err == nil {
		probe := filepath.Join(lxcCacheDir(), ".lxcpanel-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0644); err == nil {
			e.CacheWritable = true
			os.Remove(probe)
		}
	}

	// templates
	if entries, err := os.ReadDir(lxcTemplate); err == nil {
		for _, it := range entries {
			n := it.Name()
			if strings.HasPrefix(n, "lxc-") {
				t := strings.TrimPrefix(n, "lxc-")
				e.Templates = append(e.Templates, t)
				if t == "download" {
					e.HasDownload = true
				}
			}
		}
		sort.Strings(e.Templates)
	}

	// cgroup
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		e.CgroupVersion = 2
	} else if _, err := os.Stat("/sys/fs/cgroup/memory"); err == nil {
		e.CgroupVersion = 1
	}
	if e.CgroupVersion == 2 {
		probe := filepath.Join("/sys/fs/cgroup", fmt.Sprintf("lxcpanel-probe-%d", time.Now().UnixNano()))
		if err := os.Mkdir(probe, 0755); err == nil {
			e.CgroupWritable = true
			os.Remove(probe)
		}
	}

	if haveCmd("apt-get") {
		e.PkgManager = "apt"
	} else if haveCmd("dnf") {
		e.PkgManager = "dnf"
	} else if haveCmd("apk") {
		e.PkgManager = "apk"
	}

	if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		e.Kernel = strings.TrimSpace(string(raw))
	}
	if raw, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok && k == "PRETTY_NAME" {
				e.OSName = strings.Trim(strings.TrimSpace(v), `"`)
			}
		}
	}

	if names, err := lxcListNames(); err == nil {
		e.Containers = len(names)
	}

	if !e.Installed {
		e.Problems = append(e.Problems, "未检测到完整的 LXC 命令（lxc-ls / lxc-info / lxc-create 等）")
		e.Hints = append(e.Hints, "点击「安装 LXC 运行环境」自动安装，或在 SSH 中执行："+installCmd(e.PkgManager))
	}
	if e.Installed && !e.HasDownload {
		e.Problems = append(e.Problems, "缺少 lxc-download 模板，无法在线创建发行版容器")
		e.Hints = append(e.Hints, "安装 lxc-templates 软件包：apt-get install -y lxc-templates")
	}
	// download 模板运行依赖 wget / tar / xz
	var missingTools []string
	for _, t := range []string{"wget", "tar", "xz"} {
		if !haveCmd(t) {
			missingTools = append(missingTools, t)
		}
	}
	if len(missingTools) > 0 {
		e.Problems = append(e.Problems, "缺少创建容器所需的命令："+strings.Join(missingTools, "、"))
		if e.PkgManager == "apt" {
			e.Hints = append(e.Hints, "apt-get install -y "+strings.Join(missingTools, " "))
		}
	}
	if !e.Root {
		e.Problems = append(e.Problems, "当前进程不是 root，LXC 需要 root 权限才能管理容器")
		e.Hints = append(e.Hints, "请确认 config/privilege 中已设置 run-as=root")
	}
	if e.CgroupVersion == 0 {
		e.Problems = append(e.Problems, "未检测到 cgroup 挂载，容器可能无法启动")
	}
	if e.CgroupVersion == 2 && !e.CgroupWritable {
		e.Problems = append(e.Problems, "cgroup v2 层级不可写，可能无法创建容器")
		e.Hints = append(e.Hints, "确认 /sys/fs/cgroup 已挂载且当前用户为 root")
	}
	if !e.StateWritable {
		e.Problems = append(e.Problems, e.StateDir+" 不可写")
		e.Hints = append(e.Hints, "检查目录是否存在且当前用户可写，或在「设置」里换一个存储位置")
	}
	if !e.CacheWritable {
		e.Problems = append(e.Problems, e.CacheDir+" 不可写，创建容器时无法缓存镜像")
		e.Hints = append(e.Hints, "在「设置」里把模板缓存位置换到可写的数据盘")
	}
	return e
}

func installCmd(pm string) string {
	switch pm {
	case "apt":
		return "apt-get update && apt-get install -y lxc lxc-templates"
	case "dnf":
		return "dnf install -y lxc lxc-templates"
	case "apk":
		return "apk add lxc lxc-templates"
	}
	return "请使用系统包管理器安装 lxc 与 lxc-templates"
}

func runInstall(pm string, logf func(string)) error {
	var cmd *exec.Cmd
	switch pm {
	case "apt":
		cmd = exec.Command("sh", "-c", "export DEBIAN_FRONTEND=noninteractive; apt-get update && apt-get install -y lxc lxc-templates")
	case "dnf":
		cmd = exec.Command("sh", "-c", "dnf install -y lxc lxc-templates")
	case "apk":
		cmd = exec.Command("sh", "-c", "apk add lxc lxc-templates")
	default:
		return errors.New("未找到受支持的包管理器（apt/dnf/apk）")
	}
	logf("$ " + strings.Join(cmd.Args, " "))
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		logf(sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("安装失败: %v", err)
	}
	return nil
}

// distro catalogue used by the create wizard.
type DistroOption struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Icon     string          `json:"icon"`
	Releases []ReleaseOption `json:"releases"`
	Default  string          `json:"default"`
}

// ReleaseOption 是模板版本。ID 为传给 lxc-create 的实际值（如 bookworm），
// Label 是给人看的写法（如 12 (bookworm)）——Debian / Ubuntu 这类用代号标识
// 版本的发行版，直接显示代号很难看出新旧，所以补上版本号。
type ReleaseOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func rel(id, label string) ReleaseOption { return ReleaseOption{ID: id, Label: label} }

func distroCatalog() []DistroOption {
	return []DistroOption{
		{ID: "debian", Name: "Debian", Icon: "debian", Default: "bookworm", Releases: []ReleaseOption{
			rel("bookworm", "12 (bookworm)"), rel("bullseye", "11 (bullseye)"), rel("trixie", "13 (trixie)"),
		}},
		{ID: "ubuntu", Name: "Ubuntu", Icon: "ubuntu", Default: "noble", Releases: []ReleaseOption{
			rel("noble", "24.04 (noble)"), rel("jammy", "22.04 (jammy)"), rel("focal", "20.04 (focal)"),
		}},
		{ID: "almalinux", Name: "AlmaLinux", Icon: "alma", Default: "9", Releases: []ReleaseOption{
			rel("9", "9"), rel("8", "8"),
		}},
		{ID: "rockylinux", Name: "Rocky Linux", Icon: "rocky", Default: "9", Releases: []ReleaseOption{
			rel("9", "9"), rel("8", "8"),
		}},
		{ID: "centos", Name: "CentOS Stream", Icon: "centos", Default: "9", Releases: []ReleaseOption{
			rel("9", "9"), rel("8", "8"),
		}},
		{ID: "alpine", Name: "Alpine", Icon: "alpine", Default: "3.20", Releases: []ReleaseOption{
			rel("3.20", "3.20"), rel("3.19", "3.19"), rel("edge", "edge（滚动）"),
		}},
		{ID: "archlinux", Name: "Arch Linux", Icon: "arch", Default: "current", Releases: []ReleaseOption{
			rel("current", "current（滚动）"),
		}},
		{ID: "fedora", Name: "Fedora", Icon: "fedora", Default: "40", Releases: []ReleaseOption{
			rel("40", "40"), rel("39", "39"),
		}},
		{ID: "opensuse", Name: "openSUSE", Icon: "suse", Default: "tumbleweed", Releases: []ReleaseOption{
			rel("tumbleweed", "Tumbleweed（滚动）"), rel("15.6", "15.6 (Leap)"),
		}},
		{ID: "kali", Name: "Kali Linux", Icon: "kali", Default: "current", Releases: []ReleaseOption{
			rel("current", "current（滚动）"),
		}},
	}
}

var imageServers = []map[string]string{
	{"id": "official", "name": "官方源（海外）", "host": "images.linuxcontainers.org"},
	{"id": "tuna", "name": "清华 TUNA 镜像", "host": "mirrors.tuna.tsinghua.edu.cn/lxc-images"},
	{"id": "ustc", "name": "中科大 USTC 镜像", "host": "mirrors.ustc.edu.cn/lxc-images"},
	{"id": "aliyun", "name": "阿里云镜像", "host": "mirrors.aliyun.com/lxc-images"},
	{"id": "nju", "name": "南京大学镜像", "host": "mirror.nju.edu.cn/lxc-images"},
}
