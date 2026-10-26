package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseKV(t *testing.T) {
	out := "shadow=1\nhash=$6$abc$def\nshell=/bin/bash\nsshb=\nport=22\nmisc=a=b\n"
	kv := parseKV(out)
	if kv["shadow"] != "1" || kv["hash"] != "$6$abc$def" || kv["shell"] != "/bin/bash" {
		t.Fatalf("解析结果不对: %#v", kv)
	}
	// 空 value 要保留（用来说明「查到了但是空」）
	if v, ok := kv["sshb"]; !ok || v != "" {
		t.Fatalf("空 value 应保留，实际 ok=%v v=%q", ok, v)
	}
	// value 里含 = 不能截断
	if kv["misc"] != "a=b" {
		t.Fatalf("value 含 = 被截断了: %q", kv["misc"])
	}
}

func TestHashAlgo(t *testing.T) {
	cases := map[string]string{
		"$6$salt$hash":  "SHA-512",
		"$5$salt$hash":  "SHA-256",
		"$1$salt$hash":  "MD5",
		"$y$j9T$salt$h": "yescrypt",
		"$2b$12$salt":   "bcrypt",
		"":              "",
		"!":             "",
		"plaintext":     "",
	}
	for in, want := range cases {
		if got := hashAlgo(in); got != want {
			t.Fatalf("hashAlgo(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestHashUsable(t *testing.T) {
	if !hashUsable("$6$abcdefgh$0123456789abcdefghij") {
		t.Fatal("正常 SHA-512 哈希应判定可用")
	}
	for _, bad := range []string{"", "!", "*", "$6$", "$6$salt", "x"} {
		if hashUsable(bad) {
			t.Fatalf("hashUsable(%q) 应为 false", bad)
		}
	}
}

func TestShellUsable(t *testing.T) {
	cases := map[string]bool{
		"/bin/bash":       true,
		"/bin/sh":         true,
		"/bin/ash":        true,
		"/usr/sbin/nologin": false,
		"/bin/false":      false,
		"":                false,
	}
	for in, want := range cases {
		if got := shellUsable(in); got != want {
			t.Fatalf("shellUsable(%q) = %v，期望 %v", in, got, want)
		}
	}
}

// TestLoginScriptsSyntax 用 sh -n 校验三段脚本的语法，
// 顺便确认脚本里没有残留的 Go 格式动词。
func TestLoginScriptsSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("当前环境没有 sh，跳过语法检查")
	}
	scripts := map[string]string{
		"loginProbeScript":   loginProbeScript,
		"fixShellScript":     fixShellScript,
		"fixSSHConfigScript": fixSSHConfigScript,
	}
	for name, sc := range scripts {
		if strings.Contains(sc, "%!") {
			t.Fatalf("%s 里有未替换的格式动词:\n%s", name, sc)
		}
		if !strings.Contains(sc, "done=1") && name == "loginProbeScript" {
			t.Fatalf("%s 缺少 done=1 收尾标记", name)
		}
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(sc)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s 语法错误: %v\n%s\n---\n%s", name, err, out, sc)
		}
	}
}

// TestLoginProbeScriptKeys 确认探测脚本会把用到的每个 key 都打出来。
func TestLoginProbeScriptKeys(t *testing.T) {
	for _, k := range []string{"shadow=", "hash=", "shell=", "sshb=", "sshrun=", "port=", "listen=", "prl=", "pwa=", "init=", "pkg=", "addr="} {
		if !strings.Contains(loginProbeScript, k) {
			t.Fatalf("探测脚本缺少 %q 字段", k)
		}
	}
}
