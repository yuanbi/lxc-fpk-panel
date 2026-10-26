package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 容器的网络配置：桥接识别、IPv4/IPv6 开关、宿主机侧的转发开关。
//
// IPv6 采用 ULA 私有前缀（RFC 4193），宿主机桥接固定拿 ::1，容器从 ::2 起按
// 空闲地址分配。这样即使上游没有下发 IPv6，容器之间、容器与宿主机之间也能用
// IPv6 互通，配合端口转发还能从局域网访问。

const lxcULAPrefix = "fd42:6c78:63"

// NetConfig 是容器 config 里与网络相关的关键行。
type NetConfig struct {
	Bridge   string   `json:"bridge"`
	Veth     string   `json:"veth"`
	MAC      string   `json:"mac"`
	Type     string   `json:"type"`
	IPv4Cfg  []string `json:"ipv4_cfg"`
	IPv6Cfg  []string `json:"ipv6_cfg"`
	NoIPv6   bool     `json:"no_ipv6"`
	Disabled bool     `json:"disabled"`
}

func readNetConfig(name string) NetConfig {
	var nc NetConfig
	nc.IPv4Cfg, nc.IPv6Cfg = []string{}, []string{}
	raw, err := os.ReadFile(filepath.Join(lxcPath(), name, "config"))
	if err != nil {
		return nc
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
		switch {
		case k == "lxc.net.0.type":
			nc.Type = v
		case k == "lxc.net.0.link":
			nc.Bridge = v
		case k == "lxc.net.0.veth.pair":
			nc.Veth = v
		case k == "lxc.net.0.hwaddr":
			nc.MAC = v
		case strings.HasSuffix(k, ".ipv4.address") && strings.HasPrefix(k, "lxc.net."):
			nc.IPv4Cfg = append(nc.IPv4Cfg, v)
		case strings.HasSuffix(k, ".ipv6.address") && strings.HasPrefix(k, "lxc.net."):
			nc.IPv6Cfg = append(nc.IPv6Cfg, v)
		}
	}
	// 老模板偶尔不写 type，只要有 link 就认为走 veth
	if nc.Type == "" && nc.Bridge != "" {
		nc.Type = "veth"
	}
	nc.Disabled = nc.Type == "" || nc.Type == "none"
	return nc
}

func ipv4ForwardOn() bool { return readSysInt("/proc/sys/net/ipv4/ip_forward") == 1 }
func ipv6ForwardOn() bool { return readSysInt("/proc/sys/net/ipv6/conf/all/forwarding") == 1 }
func setIPv4Forward() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)
}
func setIPv6Forward() error {
	return os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o644)
}

func readSysInt(p string) int {
	raw, err := os.ReadFile(p)
	if err != nil {
		return -1
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

// ensureForward 打开内核转发。写 /proc/sys 失败通常是没有权限，交给调用方提示。
func ensureForward(v6 bool) (changed bool, err error) {
	if v6 {
		if ipv6ForwardOn() {
			return false, nil
		}
		return true, setIPv6Forward()
	}
	if ipv4ForwardOn() {
		return false, nil
	}
	return true, setIPv4Forward()
}

// bridgeHasULAv6 判断桥接上是否已经配好内网 IPv6 前缀。
func bridgeHasULAv6(bridge string) bool {
	out, err := runCmd(5*time.Second, "ip", "-6", "addr", "show", "dev", bridge)
	if err != nil {
		return false
	}
	return strings.Contains(out, lxcULAPrefix+":")
}

// ensureBridgeULA 给桥接加上 fd42:6c78:63::1/64，容器才能把它当 IPv6 网关。
func ensureBridgeULA(bridge string) (bool, error) {
	if bridge == "" {
		return false, errors.New("容器没有配置桥接网卡")
	}
	if bridgeHasULAv6(bridge) {
		return false, nil
	}
	if out, err := runCmd(8*time.Second, "ip", "-6", "addr", "add", lxcULAPrefix+"::1/64", "dev", bridge); err != nil {
		return false, fmt.Errorf("给 %s 添加 IPv6 地址失败: %s", bridge, strings.TrimSpace(out))
	}
	return true, nil
}

// allocULA 在 fd42:6c78:63::/64 里挑一个没被其他容器占用的地址（::2 - ::fd）。
func allocULA(name string) string {
	used := map[string]bool{}
	if names, err := lxcListNames(); err == nil {
		for _, n := range names {
			for _, a := range readNetConfig(n).IPv6Cfg {
				if ip, _, err := net.ParseCIDR(a); err == nil {
					used[ip.String()] = true
				} else {
					used[a] = true
				}
			}
		}
	}
	for i := 2; i < 254; i++ {
		addr := fmt.Sprintf("%s::%x", lxcULAPrefix, i)
		if !used[addr] {
			return addr
		}
	}
	return ""
}

// enableContainerIPv6 给容器配上内网 IPv6 地址，并把宿主机侧准备好。
// 返回实际分配的地址和操作日志；配置改动需要重启容器才生效。
func enableContainerIPv6(name string) (string, []string, error) {
	var logs []string
	nc := readNetConfig(name)
	if nc.Disabled {
		return "", nil, errors.New("该容器的网络已关闭（无 veth 网卡），请先重建容器并选择「NAT 桥接」")
	}
	if len(nc.IPv6Cfg) > 0 {
		return strings.TrimSuffix(nc.IPv6Cfg[0], "/64"), logs, nil
	}

	v6Changed, err := ensureForward(true)
	if err != nil {
		return "", logs, fmt.Errorf("开启 IPv6 转发失败: %v", err)
	}
	if v6Changed {
		logs = append(logs, "已开启宿主机 IPv6 转发（net.ipv6.conf.all.forwarding=1）")
	}
	if v4Changed, err4 := ensureForward(false); err4 == nil && v4Changed {
		logs = append(logs, "已开启宿主机 IPv4 转发（net.ipv4.ip_forward=1）")
	}
	bridge := nc.Bridge
	if bridge == "" {
		bridge = "lxcbr0"
	}
	if added, err := ensureBridgeULA(bridge); err != nil {
		return "", logs, err
	} else if added {
		logs = append(logs, fmt.Sprintf("已在 %s 上配置内网 IPv6 网关 %s::1/64", bridge, lxcULAPrefix))
	}

	addr := allocULA(name)
	if addr == "" {
		return "", logs, errors.New("内网 IPv6 地址池已用尽（/64 里最多 252 个容器）")
	}
	if err := setConfigKey(name, "lxc.net.0.ipv6.address", addr+"/64"); err != nil {
		return "", logs, fmt.Errorf("写入 IPv6 地址失败: %v", err)
	}
	if err := setConfigKey(name, "lxc.net.0.ipv6.gateway", lxcULAPrefix+"::1"); err != nil {
		return "", logs, fmt.Errorf("写入 IPv6 网关失败: %v", err)
	}
	logs = append(logs, "已为容器分配 IPv6 地址 "+addr+"/64，网关 "+lxcULAPrefix+"::1")
	return addr, logs, nil
}

func disableContainerIPv6(name string) error {
	removeConfigKey(name, "lxc.net.0.ipv6.address")
	removeConfigKey(name, "lxc.net.0.ipv6.gateway")
	return nil
}

// removeNetKeys 清掉全部 lxc.net.* 配置，用于创建时选择「无网络」。
func removeNetKeys(name string) error {
	path := filepath.Join(lxcPath(), name, "config")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var keep []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "lxc.net.") {
			continue
		}
		keep = append(keep, line)
	}
	return os.WriteFile(path, []byte(strings.Join(keep, "\n")), 0o644)
}

// applyNetworkOptions 在 lxc-create 之后按创建参数调整网络。
func applyNetworkOptions(req CreateRequest, logf func(string)) {
	if req.Network == "none" {
		if err := removeNetKeys(req.Name); err != nil {
			logf("关闭容器网络失败: " + err.Error())
		} else {
			logf("网络: 已关闭（容器只有 lo，无法访问外网，也无法被端口转发）")
		}
		return
	}
	nc := readNetConfig(req.Name)
	if nc.Bridge != "" {
		logf("网络: veth 桥接到 " + nc.Bridge + "（NAT 出网）")
	}
	if req.IPv6 == "on" {
		addr, logs, err := enableContainerIPv6(req.Name)
		for _, l := range logs {
			logf(l)
		}
		if err != nil {
			logf("启用 IPv6 失败: " + err.Error())
		} else {
			logf("IPv6: " + addr + "/64 → 网关 " + lxcULAPrefix + "::1")
		}
	}
}
