package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 直接读 cgroup 拿到容器的内存与 CPU 用量。
//
// 为什么不用 lxc-info 的值：lxc-info 的 Memory use / CPU use 两行依赖它自己去
// 找 cgroup 路径，个别系统（cgroup v2 + systemd 驱动、或 cgroup 树被裁剪）上
// 会给出 0，甚至卡在这一步。容器 init 进程的 /proc/<pid>/cgroup 一定准确，
// 顺着它落到 /sys/fs/cgroup 下就是容器自己的 cgroup，读文件即可。

// procCgroupRel 返回 pid 对应 cgroup 的相对路径。
// cgroup v2 只有一行 "0::/lxc.payload.foo"；v1 是多行 "<id>:<controllers>:<path>"。
func procCgroupRel(pid int, controller string) (v2 bool, rel string, ok bool) {
	if pid <= 0 {
		return false, "", false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return false, "", false
	}
	return parseProcCgroup(string(raw), controller)
}

// parseProcCgroup 从 /proc/<pid>/cgroup 内容里取出指定控制器所在的 cgroup 路径。
// v2 是统一层级（"0::/path"），v1 每个控制器一行（"5:cpu,cpuacct:/path"）。
func parseProcCgroup(text, controller string) (v2 bool, rel string, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		id, ctrls, p := parts[0], parts[1], parts[2]
		if id == "0" && ctrls == "" {
			return true, p, p != ""
		}
		for _, c := range strings.Split(ctrls, ",") {
			if c == controller {
				return false, p, p != ""
			}
		}
	}
	return false, "", false
}

func readUintFile(p string) (uint64, bool) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// cgroupMemory 返回容器当前内存占用（字节）。取不到返回 false。
func cgroupMemory(pid int) (uint64, bool) {
	v2, rel, ok := procCgroupRel(pid, "memory")
	if !ok {
		return 0, false
	}
	if v2 {
		return readUintFile(filepath.Join("/sys/fs/cgroup", rel, "memory.current"))
	}
	return readUintFile(filepath.Join("/sys/fs/cgroup/memory", rel, "memory.usage_in_bytes"))
}

// cgroupCPUSeconds 返回容器累计 CPU 时间（秒）。cgroup v2 用 cpu.stat 的
// usage_usec，v1 用 cpuacct.usage（纳秒）。
func cgroupCPUSeconds(pid int) (float64, bool) {
	v2, rel, ok := procCgroupRel(pid, "cpu")
	if ok {
		if v2 {
			raw, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", rel, "cpu.stat"))
			if err == nil {
				for _, line := range strings.Split(string(raw), "\n") {
					f := strings.Fields(line)
					if len(f) == 2 && f[0] == "usage_usec" {
						if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
							return float64(n) / 1e6, true
						}
					}
				}
			}
			return 0, false
		}
		if n, ok := readUintFile(filepath.Join("/sys/fs/cgroup/cpuacct", rel, "cpuacct.usage")); ok {
			return float64(n) / 1e9, true
		}
	}
	// v1 里 cpu 与 cpuacct 可能是分开的两行。
	if _, rel2, ok2 := procCgroupRel(pid, "cpuacct"); ok2 {
		if n, ok := readUintFile(filepath.Join("/sys/fs/cgroup/cpuacct", rel2, "cpuacct.usage")); ok {
			return float64(n) / 1e9, true
		}
	}
	return 0, false
}

// cgroupDir 返回容器 cgroup 在 /sys/fs/cgroup 下的绝对路径，供诊断展示。
func cgroupDir(pid int) string {
	v2, rel, ok := procCgroupRel(pid, "memory")
	if !ok {
		return ""
	}
	base := "/sys/fs/cgroup"
	if !v2 {
		base = "/sys/fs/cgroup/memory"
	}
	return filepath.Join(base, rel)
}
