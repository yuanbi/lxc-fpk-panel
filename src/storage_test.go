package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeStorageDir(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{"", defaultLXCStateDir, ""},
		{"   ", defaultLXCStateDir, ""},
		{"/var/lib/lxc", "/var/lib/lxc", ""},
		{"/vol1/lxc", "/vol1/lxc", ""},
		{"/vol1/lxc/", "/vol1/lxc", ""},
		{"/vol1//lxc", "/vol1/lxc", ""},
		{"/vol1/媒体/容器", "/vol1/媒体/容器", ""},

		{"vol1/lxc", "", "绝对路径"},
		{"/", "", "根目录"},
		{"//", "", "根目录"},
		{"/vol1", "", "两级"},
		{"/proc/lxc", "", "系统目录"},
		{"/tmp/lxc", "", "系统目录"},
		{"/var/log/lxc", "", "系统目录"},
		{"/etc/lxc", "", "系统目录"},
		{"/vol1/a b", "", "空格"},
		{"/vol1/a\tb", "", "空格"},
		{"/vol1/../etc/lxc", "", ".."},
		{"/vol1/a'b", "", "引号"},
		{"/vol1/a\"b", "", "引号"},
		{"/vol1/a,b", "", "引号"},
		{"/vol1/a\\b", "", "引号"},
	}
	for _, c := range cases {
		got, err := normalizeStorageDir(c.in)
		if c.wantErr != "" {
			if err == nil {
				t.Errorf("normalizeStorageDir(%q) = %q, 期望报错含 %q", c.in, got, c.wantErr)
				continue
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("normalizeStorageDir(%q) 报错 %q, 期望含 %q", c.in, err.Error(), c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeStorageDir(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeStorageDir(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestRewriteRootfsBase(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "box"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "box", "config")
	body := "lxc.uts.name = box\nlxc.rootfs.path = dir:/old/lxc/box/rootfs\nlxc.arch = amd64\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, err := rewriteRootfsBase(dir, "box", "/old/lxc", "/new/lxc")
	if err != nil {
		t.Fatalf("rewrite 失败: %v", err)
	}
	if !ok {
		t.Fatal("rewrite 返回未修改，但配置里确实有旧路径")
	}
	raw, _ := os.ReadFile(cfg)
	if !strings.Contains(string(raw), "dir:/new/lxc/box/rootfs") {
		t.Fatalf("rootfs 路径未改写: %s", raw)
	}
	if strings.Contains(string(raw), "/old/lxc") {
		t.Fatalf("仍残留旧路径: %s", raw)
	}

	// 再改回去（回滚路径）
	ok, err = rewriteRootfsBase(dir, "box", "/new/lxc", "/old/lxc")
	if err != nil || !ok {
		t.Fatalf("回滚失败: ok=%v err=%v", ok, err)
	}
	raw, _ = os.ReadFile(cfg)
	if !strings.Contains(string(raw), "dir:/old/lxc/box/rootfs") {
		t.Fatalf("回滚后路径不正确: %s", raw)
	}
}

func TestRootfsBackend(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plain"), 0o755)
	os.MkdirAll(filepath.Join(dir, "overlay"), 0o755)
	os.WriteFile(filepath.Join(dir, "plain", "config"),
		[]byte("lxc.rootfs.path = dir:/var/lib/lxc/plain/rootfs\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "overlay", "config"),
		[]byte("lxc.rootfs.path = overlayfs:/var/lib/lxc/a:/var/lib/lxc/b\n"), 0o644)

	if b, _ := rootfsBackend(dir, "plain"); b != "dir" {
		t.Errorf("plain backend = %q, 期望 dir", b)
	}
	if b, _ := rootfsBackend(dir, "overlay"); b != "overlayfs" {
		t.Errorf("overlay backend = %q, 期望 overlayfs", b)
	}
}

func TestContainersIn(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, withConfig bool) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if withConfig {
			os.WriteFile(filepath.Join(p, "config"), []byte("lxc.uts.name = "+name+"\n"), 0o644)
		}
	}
	mk("box", true)
	mk("web-01", true)
	mk("lxc.monitor.box", false) // 运行态临时目录，没有 config
	mk(".hidden", true)
	mk("_bad", true)

	got := containersIn(dir)
	if len(got) != 2 || got[0] != "box" || got[1] != "web-01" {
		t.Fatalf("containersIn = %v, 期望 [box web-01]", got)
	}
	if n := containersIn(filepath.Join(dir, "missing")); n != nil {
		t.Fatalf("不存在的目录应返回 nil, 得到 %v", n)
	}
}
