package main

import (
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
)

func TestNormalizeProxy(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"", "", false},
		{"   ", "", false},
		{"http://10.0.3.1:7890", "http://10.0.3.1:7890", false},
		{"10.0.3.1:7890", "http://10.0.3.1:7890", false},
		{"10.0.3.1", "http://10.0.3.1:7890", false},
		{"socks5://10.0.3.1", "socks5://10.0.3.1:1080", false},
		{"socks5h://10.0.3.1:1081", "socks5h://10.0.3.1:1081", false},
		{"ftp://10.0.3.1:21", "", true},
		{"://bad", "", true},
	}
	for _, c := range cases {
		got, err := normalizeProxy(c.in)
		if c.err {
			if err == nil {
				t.Fatalf("normalizeProxy(%q) 期望报错，实际得到 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeProxy(%q) 报错: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("normalizeProxy(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestEnvironmentBlock(t *testing.T) {
	if environmentBlock("") != "" {
		t.Fatal("空代理不应生成任何内容")
	}
	b := environmentBlock("http://10.0.3.1:7890")
	for _, want := range []string{
		proxyMarkStart, proxyMarkEnd,
		"http_proxy=http://10.0.3.1:7890",
		"HTTPS_PROXY=http://10.0.3.1:7890",
		"no_proxy=",
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("environmentBlock 缺少 %q:\n%s", want, b)
		}
	}
	// 宿主机与内网必须直连，否则容器连面板 / 网关都会被绕进代理
	if !strings.Contains(noProxyList(), "10.0.0.0/8") {
		t.Fatal("no_proxy 应包含 10.0.0.0/8")
	}
}

func TestAptBlock(t *testing.T) {
	b := aptBlock("http://10.0.3.1:7890")
	if !strings.Contains(b, `Acquire::http::Proxy "http://10.0.3.1:7890";`) {
		t.Fatalf("apt 配置不对:\n%s", b)
	}
	if !strings.Contains(b, "Acquire::https::Proxy") {
		t.Fatalf("apt 配置缺 https:\n%s", b)
	}
	if aptBlock("") != "" {
		t.Fatal("空代理不应生成 apt 配置")
	}
}

// proxyScript 把内容 base64 后塞进 shell，必须能原样解回来。
func TestProxyScriptEncodesContent(t *testing.T) {
	proxy := "http://10.0.3.1:7890"
	sc := proxyScript(proxy)
	if !strings.Contains(sc, "PROXY_SET") {
		t.Fatal("脚本缺少成功标记")
	}
	// 抽出 ENVB64 的值并解码，确认和 environmentBlock 一致
	i := strings.Index(sc, "ENVB64='")
	if i < 0 {
		t.Fatal("脚本里找不到 ENVB64")
	}
	rest := sc[i+len("ENVB64='"):]
	j := strings.Index(rest, "'")
	raw, err := base64.StdEncoding.DecodeString(rest[:j])
	if err != nil {
		t.Fatalf("ENVB64 不是合法 base64: %v", err)
	}
	if string(raw) != environmentBlock(proxy) {
		t.Fatalf("解码内容与 environmentBlock 不一致:\n%s", string(raw))
	}
}

// 清除代理时三个文件都要删掉，不能只删 /etc/environment。
func TestProxyScriptClearsFiles(t *testing.T) {
	sc := proxyScript("")
	if !strings.Contains(sc, "/etc/apt/apt.conf.d/99lxcpanel-proxy") ||
		!strings.Contains(sc, "/etc/profile.d/lxcpanel-proxy.sh") {
		t.Fatal("清除代理时没有删除 apt / profile.d 配置")
	}
	if !strings.Contains(sc, "PROXY_CLEARED") {
		t.Fatal("清除模式缺少标记")
	}
	// 空代理时三个 base64 变量都应该为空
	for _, k := range []string{"ENVB64=''", "APTB64=''", "PROFB64=''"} {
		if !strings.Contains(sc, k) {
			t.Fatalf("空代理时 %s 应为空", k)
		}
	}
}

// 生成的脚本交给 shell 做一次语法检查，顺便确认没有残留的格式动词。
func TestProxyScriptSyntax(t *testing.T) {
	for _, proxy := range []string{"http://10.0.3.1:7890", ""} {
		sc := proxyScript(proxy)
		if strings.Contains(sc, "%!") {
			t.Fatalf("脚本里有未替换的格式动词（proxy=%q）:\n%s", proxy, sc)
		}
		if n := strings.Count(sc, proxyMarkStart); n != 2 {
			t.Fatalf("标记 START 应出现 2 次（sed + bashrc），实际 %d 次:\n%s", n, sc)
		}
		if n := strings.Count(sc, proxyMarkEnd); n != 2 {
			t.Fatalf("标记 END 应出现 2 次，实际 %d 次", n)
		}
		sh, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("当前环境没有 sh，跳过语法检查")
		}
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(sc)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("脚本语法错误（proxy=%q）: %v\n%s\n---\n%s", proxy, err, out, sc)
		}
	}
}
