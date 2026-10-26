package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeCacheDir(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{"", defaultLXCCacheDir, ""},
		{"  ", defaultLXCCacheDir, ""},
		{"/var/cache/lxc", "/var/cache/lxc", ""},
		{"/var/cache/lxc/download", "/var/cache/lxc/download", ""},
		{"/vol1/lxc-cache", "/vol1/lxc-cache", ""},

		{"/var/cache", "", "系统目录"},
		{"/var/cache/other", "", "系统目录"},
		{"/tmp/lxc", "", "系统目录"},
		{"/etc/lxc", "", "系统目录"},
		{"vol1/cache", "", "绝对路径"},
		{"/vol1", "", "两级"},
		{"/vol1/a b", "", "空格"},
	}
	for _, c := range cases {
		got, err := normalizeCacheDir(c.in)
		if c.wantErr != "" {
			if err == nil {
				t.Errorf("normalizeCacheDir(%q) = %q, 期望报错含 %q", c.in, got, c.wantErr)
				continue
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("normalizeCacheDir(%q) 报错 %q, 期望含 %q", c.in, err.Error(), c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeCacheDir(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeCacheDir(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestContainerDiskAndLoopRewrite(t *testing.T) {
	base := t.TempDir()
	oldPath := lxcPath()
	setLXCPath(base)
	defer setLXCPath(oldPath)

	// dir 存储
	if err := os.MkdirAll(filepath.Join(base, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(base, "plain", "config"),
		[]byte("lxc.rootfs.path = dir:"+filepath.Join(base, "plain", "rootfs")+"\n"), 0o644)
	if mode, size := containerDisk("plain"); mode != "dir" || size != 0 {
		t.Errorf("plain = (%q,%d), 期望 (dir,0)", mode, size)
	}

	// loop 存储
	boxDir := filepath.Join(base, "box")
	os.MkdirAll(boxDir, 0o755)
	img := filepath.Join(boxDir, diskImageName)
	f, err := os.Create(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(3 * gib); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// 配置里一律写斜杠路径（生产环境是 Linux，测试在 Windows 上跑时需要转换）
	os.WriteFile(filepath.Join(boxDir, "config"),
		[]byte("lxc.rootfs.path = loop:"+filepath.ToSlash(img)+"\n"), 0o644)

	if mode, size := containerDisk("box"); mode != "loop" || size != 3*gib {
		t.Errorf("box = (%q,%d), 期望 (loop,%d)", mode, size, 3*gib)
	}

	// 迁移时 loop: 的镜像路径也要一起改写
	target := filepath.Join(base, "moved")
	os.MkdirAll(target, 0o755)
	os.Rename(boxDir, filepath.Join(target, "box"))
	ok, err := rewriteRootfsBase(target, "box", filepath.ToSlash(base), filepath.ToSlash(target))
	if err != nil || !ok {
		t.Fatalf("改写 loop 路径失败: ok=%v err=%v", ok, err)
	}
	raw, _ := os.ReadFile(filepath.Join(target, "box", "config"))
	want := "loop:" + filepath.ToSlash(filepath.Join(target, "box", diskImageName))
	if !strings.Contains(string(raw), want) {
		t.Fatalf("loop 路径未改写: %s", raw)
	}
	if strings.Contains(string(raw), filepath.ToSlash(base)+"/box") {
		t.Fatalf("仍残留旧路径: %s", raw)
	}
}
