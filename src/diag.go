package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 容器信息读不出来时（系统名空白、内存 0 B、CPU 一直 0%）能一键导出的排查报告。
// 里面全是事实数据，不猜结论，方便直接贴出来定位。
func containerDiagText(name string) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("LXCPanel %s · %s/%s · euid=%d", appVersion, runtime.GOOS, runtime.GOARCH, os.Geteuid())
	w("容器目录: %s", lxcPath())
	w("缓存目录: %s", lxcCacheDir())
	if v, err := runCmd(5*time.Second, "lxc-version"); err == nil {
		w("LXC: %s", strings.TrimSpace(v))
	} else {
		w("LXC: 读取失败 (%v)", err)
	}
	w("")

	start := time.Now()
	infoOut, infoErr := runCmd(12*time.Second, "lxc-info", append(lxcPathArgs(), "-n", name, "-H")...)
	w("== lxc-info -n %s -H （耗时 %d ms）==", name, time.Since(start).Milliseconds())
	if strings.TrimSpace(infoOut) == "" {
		w("(无输出)")
	} else {
		w("%s", strings.TrimSpace(infoOut))
	}
	if infoErr != nil {
		w("错误: %v", infoErr)
	}
	w("")

	as, memLimit, cpuLimit, rootfs, arch, priv := readContainerConfig(name)
	w("== 解析结果 ==")
	w("autostart=%v memory.max=%d cpu.max=%v rootfs=%q arch=%q privileged=%v", as, memLimit, cpuLimit, rootfs, arch, priv)
	if raw, err := os.ReadFile(filepath.Join(lxcPath(), name, "config")); err == nil {
		w("-- config 原文（前 4000 字节）--")
		s := string(raw)
		if len(s) > 4000 {
			s = s[:4000] + "\n...(截断)"
		}
		w("%s", strings.TrimRight(s, "\n"))
	} else {
		w("config 读取失败: %v", err)
	}
	w("")

	c, _ := lxcInfo(name)
	w("== 根文件系统候选 ==")
	for _, dir := range rootfsCandidates(name, rootfs) {
		st, err := os.Stat(dir)
		if err != nil {
			w("- %s → %v", dir, err)
			continue
		}
		kind := "文件"
		if st.IsDir() {
			kind = "目录"
		}
		osr := filepath.Join(dir, "etc", "os-release")
		body, oerr := os.ReadFile(osr)
		if oerr != nil {
			w("- %s → %s，os-release: %v", dir, kind, oerr)
			continue
		}
		first := strings.SplitN(strings.TrimSpace(string(body)), "\n", 2)[0]
		w("- %s → %s，os-release 首行: %s", dir, kind, first)
	}
	w("")

	if c != nil && c.PID > 0 {
		w("== cgroup（pid %d）==", c.PID)
		if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", c.PID)); err == nil {
			w("-- /proc/%d/cgroup --", c.PID)
			w("%s", strings.TrimSpace(string(raw)))
		} else {
			w("/proc/%d/cgroup 读取失败: %v", c.PID, err)
		}
		if dir := cgroupDir(c.PID); dir != "" {
			w("cgroup 目录: %s", dir)
			if raw, err := os.ReadFile(filepath.Join(dir, "memory.current")); err == nil {
				w("memory.current = %s", strings.TrimSpace(string(raw)))
			} else {
				w("memory.current 读取失败: %v", err)
			}
			if raw, err := os.ReadFile(filepath.Join(dir, "cpu.stat")); err == nil {
				lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
				if len(lines) > 4 {
					lines = lines[:4]
				}
				w("cpu.stat: %s", strings.Join(lines, " | "))
			}
			if raw, err := os.ReadFile(filepath.Join(dir, "memory.max")); err == nil {
				w("memory.max = %s", strings.TrimSpace(string(raw)))
			}
		} else {
			w("cgroup 目录: 未识别")
		}
		if v, ok := cgroupMemory(c.PID); ok {
			w("直读内存 = %d 字节", v)
		} else {
			w("直读内存 = 失败")
		}
		if v, ok := cgroupCPUSeconds(c.PID); ok {
			w("直读 CPU = %.2f 秒", v)
		} else {
			w("直读 CPU = 失败")
		}
	} else {
		w("== cgroup == 容器未运行，无 PID")
	}
	w("")

	w("== 最终展示值 ==")
	if c != nil {
		pretty, id, ver := resolveOS(name, rootfs, c.State == "RUNNING")
		w("state=%s pid=%d mem_bytes=%d cpu_seconds=%.2f", c.State, c.PID, c.MemBytes, c.CPUSeconds)
		w("os=%q id=%q version=%q", pretty, id, ver)
	}
	return b.String()
}
