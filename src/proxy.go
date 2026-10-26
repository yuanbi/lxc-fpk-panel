package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// 容器出网代理。
//
// 国内网络里 `ping 8.8.8.8` 通、`apt update` 也通，但 `curl https://raw.githubusercontent.com`
// 报 `Recv failure: Connection reset by peer` / `GnuTLS: Error in the pull function`，
// 这不是容器网络没配好，而是国际站点在 TLS 握手阶段被重置。给容器配一个代理是最省事的解法。
//
// 宿主机上跑着 mihomo / clash 的话，只要它的 http 端口监听在网桥上
// （`allow-lan: true`，bind-address `*`），容器里填 `http://10.0.3.1:7890` 就能用。

const proxyMarkStart = "# >>> lxcpanel proxy"
const proxyMarkEnd = "# <<< lxcpanel proxy"

// normalizeProxy 校验并规范化代理地址；空字符串表示「关闭代理」。
func normalizeProxy(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", errors.New("代理地址格式不对，示例：http://10.0.3.1:7890")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", errors.New("代理只支持 http:// / https:// / socks5:// 前缀")
	}
	if u.Port() == "" {
		if strings.HasPrefix(strings.ToLower(u.Scheme), "socks") {
			u.Host += ":1080"
		} else {
			u.Host += ":7890"
		}
	}
	return u.String(), nil
}

// noProxyList 是不走代理的地址：容器本地、宿主机网段、以及面板/内网常用段。
func noProxyList() string {
	return "localhost,127.0.0.1,::1,0.0.0.0,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,*.local,*.lan"
}

// environmentBlock 生成要追加到 /etc/environment 的内容（systemd 与 PAM 都会读它）。
func environmentBlock(proxy string) string {
	if proxy == "" {
		return ""
	}
	np := noProxyList()
	var b strings.Builder
	b.WriteString(proxyMarkStart + "\n")
	for _, k := range []string{"http_proxy", "https_proxy"} {
		b.WriteString(k + "=" + proxy + "\n")
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY"} {
		b.WriteString(k + "=" + proxy + "\n")
	}
	b.WriteString("all_proxy=" + proxy + "\n")
	b.WriteString("ALL_PROXY=" + proxy + "\n")
	b.WriteString("no_proxy=" + np + "\n")
	b.WriteString("NO_PROXY=" + np + "\n")
	b.WriteString(proxyMarkEnd + "\n")
	return b.String()
}

// aptBlock 生成 apt 代理配置。apt 不读环境变量，必须单独写 apf.conf.d。
func aptBlock(proxy string) string {
	if proxy == "" {
		return ""
	}
	return fmt.Sprintf("Acquire::http::Proxy %q;\nAcquire::https::Proxy %q;\n", proxy, proxy)
}

// profileBlock 生成 /etc/profile.d 脚本，让交互式 shell 立刻带上代理变量。
func profileBlock(proxy string) string {
	if proxy == "" {
		return ""
	}
	return fmt.Sprintf("export http_proxy=%q\nexport https_proxy=%q\nexport HTTP_PROXY=%q\n"+
		"export HTTPS_PROXY=%q\nexport all_proxy=%q\nexport ALL_PROXY=%q\n"+
		"export no_proxy=%q\nexport NO_PROXY=%q\n",
		proxy, proxy, proxy, proxy, proxy, proxy, noProxyList(), noProxyList())
}

// proxyScript 生成在容器内落地的 shell 脚本。内容一律 base64，避免引号/换行被 shell 吃掉。
//
// 落点有四个，覆盖不同登录方式：
//   - /etc/environment         → sshd / PAM 登录、systemd 服务
//   - /etc/profile.d/*.sh      → 登录 shell（面板「终端」就是 lxc-attach + shell -l）
//   - /etc/bash.bashrc         → 交互式 shell
//   - /root/.bashrc            → root 的交互式 shell
func proxyScript(proxy string) string {
	enc := func(s string) string {
		if s == "" {
			return ""
		}
		return base64.StdEncoding.EncodeToString([]byte(s))
	}
	return fmt.Sprintf(`set -e
ENVB64='%s'
APTB64='%s'
PROFB64='%s'
if command -v base64 >/dev/null 2>&1; then :; else echo 'NO_BASE64'; exit 3; fi

# 先清掉上一版写的东西（幂等，重复设置不会堆积）
for f in /etc/environment /etc/bash.bashrc /root/.bashrc; do
  [ -f "$f" ] || continue
  sed -i '/^%s$/,/^%s$/d' "$f" || true
done
rm -f /etc/apt/apt.conf.d/99lxcpanel-proxy /etc/profile.d/lxcpanel-proxy.sh

if [ -n "$ENVB64" ]; then
  printf '%%s' "$ENVB64" | base64 -d >> /etc/environment
  mkdir -p /etc/apt/apt.conf.d /etc/profile.d
  printf '%%s' "$APTB64" | base64 -d > /etc/apt/apt.conf.d/99lxcpanel-proxy
  printf '%%s' "$PROFB64" | base64 -d > /etc/profile.d/lxcpanel-proxy.sh
  chmod 644 /etc/apt/apt.conf.d/99lxcpanel-proxy /etc/profile.d/lxcpanel-proxy.sh
  # 交互式 shell 不读 profile.d，补进 bashrc
  for f in /etc/bash.bashrc /root/.bashrc; do
    [ -f "$f" ] || : > "$f"
    printf '\n%%s\n' '%s' >> "$f"
    printf '%%s' "$PROFB64" | base64 -d >> "$f"
    printf '%%s\n' '%s' >> "$f"
  done
  echo 'PROXY_SET'
else
  echo 'PROXY_CLEARED'
fi`, enc(environmentBlock(proxy)), enc(aptBlock(proxy)), enc(profileBlock(proxy)),
		proxyMarkStart, proxyMarkEnd, proxyMarkStart, proxyMarkEnd)
}

// applyContainerProxy 把代理写进容器（/etc/environment、apt、profile.d）。
// proxy 为空表示清除。容器必须是运行状态——文件在容器的 rootfs 里，只有跑起来才挂得上。
func applyContainerProxy(name, proxy string) (string, error) {
	c, err := lxcInfo(name)
	if err != nil {
		return "", err
	}
	if c.State != "RUNNING" {
		return "", errors.New("容器未运行")
	}
	out, err := attachRun(name, proxyScript(proxy), "", 25*time.Second)
	out = strings.TrimSpace(out)
	if err != nil {
		if out == "" {
			out = err.Error()
		}
		return "", errors.New(out)
	}
	if strings.Contains(out, "NO_BASE64") {
		return "", errors.New("容器里没有 base64 命令，跳过代理写入")
	}
	if proxy == "" {
		return "已清除代理配置", nil
	}
	return "已写入代理 " + proxy, nil
}

// applyProxyToRunning 把代理应用给所有运行中的容器。
func applyProxyToRunning(proxy string, add func(string)) {
	names, err := lxcListNames()
	if err != nil {
		if add != nil {
			add("读取容器列表失败: " + err.Error())
		}
		return
	}
	done := 0
	for _, n := range names {
		msg, err := applyContainerProxy(n, proxy)
		if err != nil {
			continue // 停着的容器跳过，启动时会再写
		}
		done++
		if add != nil {
			add("容器 " + n + "：" + msg)
		}
	}
	if add != nil && done == 0 {
		add("没有运行中的容器，代理会在容器启动后自动写入")
	}
}

// proxyURL 返回当前配置的容器代理（空表示不配）。
func (a *App) proxyURL() string {
	return strings.TrimSpace(a.getConfig().ProxyURL)
}

// applyProxy 把面板里配的代理写进某个容器；没配代理就什么也不做。
func (a *App) applyProxy(name string, logf func(string)) {
	p := a.proxyURL()
	if p == "" {
		return
	}
	if logf != nil {
		logf("正在写入容器代理 " + p + " ...")
	}
	msg, err := applyContainerProxy(name, p)
	if err != nil {
		if logf != nil {
			logf("容器代理未写入：" + err.Error() + "（容器启动后会自动重试）")
		}
		return
	}
	if logf != nil {
		logf("容器 " + name + "：" + msg)
	}
}
