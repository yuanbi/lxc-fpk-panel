package main

import (
	"path/filepath"
	"testing"
)

func TestParseProcCgroup(t *testing.T) {
	v2 := "0::/lxc.payload.lxc-debian-12\n"
	gotV2, gotRel, ok := parseProcCgroup(v2, "memory")
	if !ok || !gotV2 || gotRel != "/lxc.payload.lxc-debian-12" {
		t.Fatalf("cgroup v2 解析错误: v2=%v rel=%q ok=%v", gotV2, gotRel, ok)
	}

	v1 := "12:memory:/lxc/lxc-debian-12\n11:cpu,cpuacct:/lxc/lxc-debian-12\n1:name=systemd:/init.scope\n"
	gotV2, gotRel, ok = parseProcCgroup(v1, "memory")
	if !ok || gotV2 || gotRel != "/lxc/lxc-debian-12" {
		t.Fatalf("cgroup v1 memory 解析错误: v2=%v rel=%q ok=%v", gotV2, gotRel, ok)
	}
	if ok2, rel2, _ := parseProcCgroup(v1, "cpuacct"); !ok2 && rel2 == "" {
		t.Fatalf("cgroup v1 cpuacct 应命中同一行: %q", rel2)
	}
	if _, _, ok := parseProcCgroup(v1, "blkio"); ok {
		t.Fatal("不存在的控制器不应命中")
	}
	if _, _, ok := parseProcCgroup("", "memory"); ok {
		t.Fatal("空内容不应命中")
	}
}

func TestParseOSRelease(t *testing.T) {
	pretty, id, ver := parseOSRelease(`PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
ID=debian
VERSION_ID="12"
`)
	if pretty != "Debian GNU/Linux 12 (bookworm)" || id != "debian" || ver != "12" {
		t.Fatalf("解析错误: %q %q %q", pretty, id, ver)
	}
}

// os-release 读不到时，配置里的 rootfs 写法五花八门，候选目录要能覆盖到挂载点。
func TestRootfsCandidates(t *testing.T) {
	mount := filepath.Join(lxcPath(), "box", "rootfs")

	check := func(desc, rootfs string, want []string) {
		t.Helper()
		got := rootfsCandidates("box", rootfs)
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", desc, got, want)
		}
		for i := range want {
			if filepath.Clean(got[i]) != filepath.Clean(want[i]) {
				t.Fatalf("%s: got %v, want %v", desc, got, want)
			}
		}
		if got[len(got)-1] != mount {
			t.Fatalf("%s: 最后一个候选应是挂载点 %q，实际 %q", desc, mount, got[len(got)-1])
		}
	}

	// 普通目录：配置路径与挂载点相同时只保留一份
	check("普通目录", "/var/lib/lxc/box/rootfs", []string{mount})
	check("loop 镜像", "loop:/var/lib/lxc/box/rootdev",
		[]string{"/var/lib/lxc/box/rootdev", mount})
	check("overlay 多层", "overlay:/var/lib/lxc/box/delta:/var/lib/lxc/base",
		[]string{"/var/lib/lxc/box/delta", "/var/lib/lxc/base", mount})
	check("空值只留挂载点", "", []string{mount})
	check("相对路径丢弃", "rootfs", []string{mount})
}

// 连 os-release 都读不到时，用创建时记录的信息兜底，别在列表里留一个「—」。
func TestDistroLabel(t *testing.T) {
	cases := []struct{ distro, release, want string }{
		{"debian", "bookworm", "Debian 12 (bookworm)"},
		{"ubuntu", "noble", "Ubuntu 24.04 (noble)"},
		{"almalinux", "9", "AlmaLinux 9"},
		{"debian", "", "Debian"},
		{"customos", "1.0", "Customos 1.0"},
		{"", "1.0", ""},
	}
	for _, c := range cases {
		if got := distroLabel(c.distro, c.release); got != c.want {
			t.Errorf("distroLabel(%q,%q) = %q, want %q", c.distro, c.release, got, c.want)
		}
	}
}
