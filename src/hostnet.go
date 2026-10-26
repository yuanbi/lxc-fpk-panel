package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 宿主机侧网络自愈。
//
// LXC 容器的「能上外网」依赖四件事，缺一件就 ping 不通：
//   1. 内核 IPv4 转发打开（net.ipv4.ip_forward=1）
//   2. 桥接网卡（默认 lxcbr0）存在、UP、并且有网关地址
//   3. iptables 有 MASQUERADE 出网规则 + FORWARD 放行
//   4. 桥接上跑着 DHCP/DNS（通常是 lxc-net 起的 dnsmasq），容器才能拿到 IP
//
// 飞牛 OS 上 lxc 是装机自带/手动装的，lxc-net 服务可能没启用，桥接就没人管，
// 于是容器里就出现「有 veth 但没 IP / 有 IP 但出不去」。这里把这四件事全部
// 兜住：先检查，再一键修复，修复时对老容器还会按需补静态 IP。

const (
	lxcNetBridge = "lxcbr0"
	lxcNetGW     = "10.0.3.1"
	lxcNetCIDR   = "10.0.3.1/24"
	lxcNetSubnet = "10.0.3.0/24"
	// lxcDHCPStart/End 是自带 dnsmasq 的地址池，避开网关和常见手工地址。
	lxcDHCPStart = 100
	lxcDHCPEnd   = 240
)

// dnsFallback 是没有 DHCP 时写进容器 resolv.conf 的公共 DNS。
var dnsFallback = []string{"223.5.5.5", "119.29.29.29"}

// appDataDir 由 main 在启动时写入，网络修复要在这里落 dnsmasq 配置。
var appDataDir = "/var/lib/lxc-panel"

type NetCheck struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Hint    string `json:"hint,omitempty"`
	Fixable bool   `json:"fixable"`
}

type HostNetStatus struct {
	Bridge      string     `json:"bridge"`
	Gateway     string     `json:"gateway"`
	Subnet      string     `json:"subnet"`
	OK          bool       `json:"ok"`
	HasIPTables bool       `json:"has_iptables"`
	HasDNSMasq  bool       `json:"has_dnsmasq"`
	HasSystemd  bool       `json:"has_systemd"`
	Proxy       string     `json:"proxy,omitempty"`
	Checks      []NetCheck `json:"checks"`
	Tips        []string   `json:"tips"`
}

// ---------- 基础探测 ----------

// lookBin 找一个可执行文件。服务由 systemd 拉起时 PATH 往往不含 sbin，
// 只靠 exec.LookPath 会误判成「没装 dnsmasq / iptables」。
func lookBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/local/bin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func ipLinkExists(dev string) bool {
	if dev == "" {
		return false
	}
	out, err := runCmd(5*time.Second, "ip", "link", "show", "dev", dev)
	return err == nil && strings.TrimSpace(out) != ""
}

func ipLinkUp(dev string) bool {
	out, err := runCmd(5*time.Second, "ip", "link", "show", "dev", dev)
	if err != nil {
		return false
	}
	// 只看 flags 里的 UP 令牌：桥接没有端口时 state 会是 DOWN/LOWERLAYERDOWN，
	// 但只要管理状态是 up，容器接上去就能通。
	if i, j := strings.Index(out, "<"), strings.Index(out, ">"); i >= 0 && j > i {
		for _, f := range strings.Split(out[i+1:j], ",") {
			if strings.TrimSpace(f) == "UP" {
				return true
			}
		}
	}
	return strings.Contains(out, "state UP")
}

// bridgeIPv4 返回桥接上的 IPv4 地址（CIDR 形式，如 10.0.3.1/24）。
func bridgeIPv4(dev string) []string {
	out, err := runCmd(5*time.Second, "ip", "-4", "-o", "addr", "show", "dev", dev)
	if err != nil {
		return nil
	}
	var addrs []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet" && i+1 < len(fields) {
				addrs = append(addrs, fields[i+1])
			}
		}
	}
	return addrs
}

func hasIptables() bool { return lookBin("iptables") != "" }

// ipt 返回 iptables 的可执行路径（找不到则退回命令名，让调用方得到明确的报错）。
func ipt() string {
	if p := lookBin("iptables"); p != "" {
		return p
	}
	return "iptables"
}

func natRulePresent(subnet string) bool {
	if subnet == "" || !hasIptables() {
		return false
	}
	_, err := runCmd(5*time.Second, ipt(), "-t", "nat", "-C", "POSTROUTING",
		"-s", subnet, "!", "-d", subnet, "-j", "MASQUERADE")
	return err == nil
}

func fwdRulePresent(dev string) bool {
	if dev == "" || !hasIptables() {
		return false
	}
	if _, err := runCmd(5*time.Second, ipt(), "-C", "FORWARD", "-i", dev, "-j", "ACCEPT"); err != nil {
		return false
	}
	if _, err := runCmd(5*time.Second, ipt(), "-C", "FORWARD", "-o", dev, "-j", "ACCEPT"); err != nil {
		return false
	}
	return true
}

// dhcpServing 判断桥接上是否已经有 DHCP/DNS 服务在监听。
func dhcpServing(bridge, gateway string) bool {
	// 1) udp:67 是否被绑在网关地址或通配地址上
	if ss := lookBin("ss"); ss != "" {
		if out, err := runCmd(5*time.Second, ss, "-H", "-lun"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				if !strings.Contains(line, ":67") {
					continue
				}
				if strings.Contains(line, gateway+":67") || strings.Contains(line, "0.0.0.0:67") ||
					strings.Contains(line, "*:67") || strings.Contains(line, "[::]:67") {
					return true
				}
			}
		}
	}
	// 2) 看进程：dnsmasq / dnsmasq-lxc 是否带了这块网桥
	if pg := lookBin("pgrep"); pg != "" {
		if out, err := runCmd(5*time.Second, pg, "-af", "dnsmasq"); err == nil {
			if strings.Contains(out, bridge) {
				return true
			}
		}
	}
	return false
}

// detectContainerBridge 找出容器实际在用的网桥（多个容器时取最常见的那个）。
func detectContainerBridge() string {
	count := map[string]int{}
	if names, err := lxcListNames(); err == nil {
		for _, n := range names {
			if b := readNetConfig(n).Bridge; b != "" {
				count[b]++
			}
		}
	}
	best, bestN := "", 0
	for b, c := range count {
		if c > bestN {
			best, bestN = b, c
		}
	}
	if best != "" {
		return best
	}
	return lxcNetBridge
}

// hostNetInfo 返回 (网桥, 网关地址, 网段)。桥接上没有地址时用 lxc 默认值。
func hostNetInfo() (bridge, gateway, subnet string) {
	bridge = detectContainerBridge()
	gateway, subnet = lxcNetGW, lxcNetSubnet
	if addrs := bridgeIPv4(bridge); len(addrs) > 0 {
		if ip, ipnet, err := net.ParseCIDR(addrs[0]); err == nil {
			gateway = ip.String()
			ones, _ := ipnet.Mask.Size()
			subnet = ipnet.IP.String() + "/" + strconv.Itoa(ones)
		}
	}
	return
}

// hostNetStatus 汇总宿主机侧所有会影响容器出网的检查项。
func hostNetStatus() HostNetStatus {
	bridge, gateway, subnet := hostNetInfo()
	st := HostNetStatus{
		Bridge:      bridge,
		Gateway:     gateway,
		Subnet:      subnet,
		HasIPTables: hasIptables(),
		HasDNSMasq:  lookBin("dnsmasq") != "",
		HasSystemd:  lookBin("systemctl") != "",
		Checks:      []NetCheck{},
		Tips:        []string{},
	}

	// 1. 桥接
	addrs := bridgeIPv4(bridge)
	switch {
	case !ipLinkExists(bridge):
		st.Checks = append(st.Checks, NetCheck{
			Key: "bridge", Label: "桥接网卡 " + bridge, OK: false,
			Detail: "不存在", Hint: "一键修复会创建它并配置 " + lxcNetCIDR, Fixable: true,
		})
	case len(addrs) == 0:
		st.Checks = append(st.Checks, NetCheck{
			Key: "bridge", Label: "桥接网卡 " + bridge, OK: false,
			Detail: "已存在但没有 IPv4 地址", Hint: "一键修复会补上 " + lxcNetCIDR, Fixable: true,
		})
	case !ipLinkUp(bridge):
		st.Checks = append(st.Checks, NetCheck{
			Key: "bridge", Label: "桥接网卡 " + bridge, OK: false,
			Detail: "地址 " + addrs[0] + "，但网卡未启用", Hint: "一键修复会 ip link set up", Fixable: true,
		})
	default:
		st.Checks = append(st.Checks, NetCheck{
			Key: "bridge", Label: "桥接网卡 " + bridge, OK: true,
			Detail: addrs[0] + "（已启用）",
		})
	}

	// 2. IPv4 转发
	if ipv4ForwardOn() {
		st.Checks = append(st.Checks, NetCheck{Key: "forward", Label: "内核 IPv4 转发", OK: true, Detail: "已开启"})
	} else {
		st.Checks = append(st.Checks, NetCheck{
			Key: "forward", Label: "内核 IPv4 转发", OK: false,
			Detail: "net.ipv4.ip_forward=0", Hint: "关闭时容器流量无法转发到外网", Fixable: true,
		})
	}

	// 3. NAT 出网
	if !hasIptables() {
		st.Checks = append(st.Checks, NetCheck{
			Key: "nat", Label: "NAT 出网规则", OK: false,
			Detail: "系统没有 iptables 命令", Hint: "需要先安装 iptables", Fixable: false,
		})
	} else if natRulePresent(subnet) {
		st.Checks = append(st.Checks, NetCheck{
			Key: "nat", Label: "NAT 出网规则", OK: true, Detail: "POSTROUTING MASQUERADE " + subnet,
		})
	} else {
		st.Checks = append(st.Checks, NetCheck{
			Key: "nat", Label: "NAT 出网规则", OK: false,
			Detail: "缺少 " + subnet + " 的 MASQUERADE", Hint: "没有它容器只能通到宿主机，出不去公网", Fixable: true,
		})
	}

	// 4. FORWARD 放行
	switch {
	case !hasIptables():
		st.Checks = append(st.Checks, NetCheck{
			Key: "fwd", Label: "FORWARD 放行", OK: false,
			Detail: "系统没有 iptables 命令", Hint: "需要先安装 iptables", Fixable: false,
		})
	case !fwdRulePresent(bridge):
		st.Checks = append(st.Checks, NetCheck{
			Key: "fwd", Label: "FORWARD 放行", OK: false,
			Detail: "缺少 -i/-o " + bridge + " ACCEPT", Hint: "默认 DROP 的防火墙上会拦掉容器流量", Fixable: true,
		})
	default:
		st.Checks = append(st.Checks, NetCheck{Key: "fwd", Label: "FORWARD 放行", OK: true, Detail: "已放行 " + bridge})
	}

	// 5. DHCP / DNS
	if dhcpServing(bridge, gateway) {
		st.Checks = append(st.Checks, NetCheck{
			Key: "dhcp", Label: "DHCP / DNS 服务", OK: true, Detail: "已监听 " + gateway + ":67",
		})
	} else {
		hint := "没有 DHCP，容器拿不到 IP（网卡在、地址空）"
		if !st.HasDNSMasq {
			hint = "没有 DHCP；系统未装 dnsmasq，修复时会改用静态 IP + 写死 DNS"
		}
		st.Checks = append(st.Checks, NetCheck{
			Key: "dhcp", Label: "DHCP / DNS 服务", OK: false,
			Detail: "桥接上没有 DHCP 服务", Hint: hint, Fixable: true,
		})
	}

	// 6. IPv6 转发（只影响 IPv6，不算硬伤）
	if ipv6ForwardOn() {
		st.Checks = append(st.Checks, NetCheck{Key: "forward6", Label: "内核 IPv6 转发", OK: true, Detail: "已开启"})
	} else {
		st.Checks = append(st.Checks, NetCheck{
			Key: "forward6", Label: "内核 IPv6 转发", OK: false,
			Detail: "net.ipv6.conf.all.forwarding=0", Hint: "只在容器要用 IPv6 时才需要", Fixable: true,
		})
	}

	// 7. MSS 钳制：只在宿主上行 MTU 小于 1500 时才有意义
	// （PPPoE 1492 / WireGuard 1420），此时缺这条规则会表现为「ping 通但 https 握手必挂」。
	if mtu := egressMTU(); mtu >= 576 && mtu < 1500 {
		if mssClampPresent(bridge) {
			st.Checks = append(st.Checks, NetCheck{
				Key: "mss", Label: "MSS 钳制", OK: true,
				Detail: fmt.Sprintf("出口 MTU %d，已钳到 %d", mtu, mtu-40),
			})
		} else {
			st.Checks = append(st.Checks, NetCheck{
				Key: "mss", Label: "MSS 钳制", OK: false, Fixable: true,
				Detail: fmt.Sprintf("出口 MTU 只有 %d，但没有 TCPMSS 规则", mtu),
				Hint:   "这种情况下 ping 通、http 也能下，但 https 握手会卡死（TLS 证书包被丢）",
			})
		}
	}

	st.OK = true
	for _, c := range st.Checks {
		if !c.OK && c.Key != "forward6" && c.Key != "mss" {
			st.OK = false
		}
	}
	if !st.OK {
		st.Tips = append(st.Tips, "点「一键修复」会自动补齐桥接、转发、NAT 和 DHCP；已存在的容器可能需要重启才拿到新地址。")
	}
	if !st.HasDNSMasq && !dhcpServing(bridge, gateway) {
		st.Tips = append(st.Tips, "建议安装 dnsmasq（apt install dnsmasq），这样容器可以自动拿 IP 和 DNS。")
	}
	return st
}

// ---------- 修复动作 ----------

func ipv4CIDROf(gateway string) string {
	if gateway == lxcNetGW {
		return lxcNetCIDR
	}
	return gateway + "/24"
}

func ensureBridge(bridge, gateway string, logf func(string)) error {
	if !ipLinkExists(bridge) {
		if out, err := runCmd(8*time.Second, "ip", "link", "add", bridge, "type", "bridge"); err != nil {
			return fmt.Errorf("创建网桥 %s 失败: %s", bridge, strings.TrimSpace(out))
		}
		logf("已创建网桥 " + bridge)
	}
	if len(bridgeIPv4(bridge)) == 0 {
		cidr := ipv4CIDROf(gateway)
		if out, err := runCmd(8*time.Second, "ip", "addr", "add", cidr, "dev", bridge); err != nil {
			if !strings.Contains(strings.ToLower(out), "file exists") {
				return fmt.Errorf("给 %s 添加地址 %s 失败: %s", bridge, cidr, strings.TrimSpace(out))
			}
		} else {
			logf("已给 " + bridge + " 配置网关地址 " + cidr)
		}
	}
	if !ipLinkUp(bridge) {
		if out, err := runCmd(8*time.Second, "ip", "link", "set", bridge, "up"); err != nil {
			return fmt.Errorf("启用网桥 %s 失败: %s", bridge, strings.TrimSpace(out))
		}
		logf("已启用网桥 " + bridge)
	}
	return nil
}

func ensureNAT(subnet string) (bool, error) {
	if natRulePresent(subnet) {
		return false, nil
	}
	if out, err := runCmd(8*time.Second, ipt(), "-t", "nat", "-A", "POSTROUTING",
		"-s", subnet, "!", "-d", subnet, "-j", "MASQUERADE"); err != nil {
		return false, fmt.Errorf("添加 NAT 规则失败: %s", strings.TrimSpace(out))
	}
	return true, nil
}

func ensureForwardRule(dev string) (bool, error) {
	changed := false
	if _, err := runCmd(8*time.Second, ipt(), "-C", "FORWARD", "-i", dev, "-j", "ACCEPT"); err != nil {
		if out, e := runCmd(8*time.Second, ipt(), "-I", "FORWARD", "-i", dev, "-j", "ACCEPT"); e != nil {
			return changed, fmt.Errorf("放行 %s 入向流量失败: %s", dev, strings.TrimSpace(out))
		}
		changed = true
	}
	if _, err := runCmd(8*time.Second, ipt(), "-C", "FORWARD", "-o", dev, "-j", "ACCEPT"); err != nil {
		if out, e := runCmd(8*time.Second, ipt(), "-I", "FORWARD", "-o", dev, "-j", "ACCEPT"); e != nil {
			return changed, fmt.Errorf("放行 %s 出向流量失败: %s", dev, strings.TrimSpace(out))
		}
		changed = true
	}
	return changed, nil
}

// ipBin 返回 ip 命令的绝对路径。
func ipBin() string {
	if p := lookBin("ip"); p != "" {
		return p
	}
	return "ip"
}

// egressMTU 返回宿主机默认出网网卡的 MTU（取不到返回 0）。
// PPPoE 拨号是 1492，WireGuard / 部分 VPN 是 1420 甚至更小。
func egressMTU() int {
	out, err := runCmd(5*time.Second, ipBin(), "-o", "route", "get", "223.5.5.5")
	if err != nil {
		return 0
	}
	fields := strings.Fields(out)
	dev := ""
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			dev = fields[i+1]
			break
		}
	}
	if dev == "" {
		return 0
	}
	link, err := runCmd(5*time.Second, ipBin(), "-o", "link", "show", "dev", dev)
	if err != nil {
		return 0
	}
	for _, f := range strings.Fields(link) {
		if strings.HasPrefix(f, "mtu") {
			if v, e := strconv.Atoi(strings.TrimPrefix(f, "mtu")); e == nil {
				return v
			}
		}
	}
	return 0
}

// mssClampRule 返回要加在 mangle FORWARD 上的 MSS 规则参数。
// 上行 MTU 小于 1500 时直接钉死 MSS（MTU 黑洞下 clamp-to-pmtu 不起作用）。
func mssClampRule(dev string) []string {
	rule := []string{"-t", "mangle", "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS"}
	if mtu := egressMTU(); mtu >= 576 && mtu < 1500 {
		return append(rule, "--set-mss", strconv.Itoa(mtu-40))
	}
	return append(rule, "--clamp-mss-to-pmtu")
}

// mssClampPresent 判断 MSS 钳制规则是否已经在 FORWARD 上（入向、出向各一条）。
func mssClampPresent(dev string) bool {
	if dev == "" || !hasIptables() {
		return false
	}
	base := mssClampRule(dev)
	for _, dir := range []string{"-i", "-o"} {
		args := append([]string{"-C", "FORWARD", dir, dev}, base...)
		if _, err := runCmd(5*time.Second, ipt(), args...); err != nil {
			return false
		}
	}
	return true
}

// ensureMSSClamp 给容器流量加 MSS 钳制。
// 典型症状：容器里 ping 通、http 也能下东西，但 https 握手必挂
// （GnuTLS: Error in the pull function / Recv failure: Connection reset by peer）。
// 原因是服务器回的 TLS 证书包超过实际路径 MTU 被丢弃，而 ICMP 分片通告又被防火墙吃掉。
func ensureMSSClamp(dev string) (bool, string, error) {
	if dev == "" || !hasIptables() {
		return false, "", nil
	}
	base := mssClampRule(dev)
	changed := false
	for _, dir := range []string{"-o", "-i"} {
		check := append([]string{"-C", "FORWARD", dir, dev}, base...)
		if _, err := runCmd(5*time.Second, ipt(), check...); err == nil {
			continue
		}
		add := append([]string{"-A", "FORWARD", dir, dev}, base...)
		if out, err := runCmd(8*time.Second, ipt(), add...); err != nil {
			return changed, "", fmt.Errorf("添加 MSS 钳制规则失败: %s", strings.TrimSpace(out))
		}
		changed = true
	}
	note := ""
	if mtu := egressMTU(); mtu >= 576 && mtu < 1500 {
		note = fmt.Sprintf("出口 MTU %d，TCP MSS 钉到 %d", mtu, mtu-40)
	} else {
		note = "按路径 PMTU 自动钳制"
	}
	return changed, note, nil
}

// dhcpRange 根据网段算出 dnsmasq 的地址池（只在 /24 上计算，其它掩码退回默认）。
func dhcpRange(gateway string) (string, string, string) {
	ip := net.ParseIP(gateway).To4()
	if ip == nil {
		return "10.0.3." + strconv.Itoa(lxcDHCPStart), "10.0.3." + strconv.Itoa(lxcDHCPEnd), "255.255.255.0"
	}
	base := fmt.Sprintf("%d.%d.%d.", ip[0], ip[1], ip[2])
	return base + strconv.Itoa(lxcDHCPStart), base + strconv.Itoa(lxcDHCPEnd), "255.255.255.0"
}

// ensureDHCP 让桥接上出现 DHCP/DNS：先试 lxc-net 服务，再退回自带 dnsmasq。
func ensureDHCP(bridge, gateway string, logf func(string)) error {
	if dhcpServing(bridge, gateway) {
		return nil
	}
	// 1) 系统自带 lxc-net（Debian/Ubuntu 装 lxc 时带的 systemd 服务）
	if systemctl := lookBin("systemctl"); systemctl != "" {
		if _, err := runCmd(25*time.Second, systemctl, "start", "lxc-net"); err == nil {
			time.Sleep(1500 * time.Millisecond)
			if dhcpServing(bridge, gateway) {
				logf("已启动系统服务 lxc-net（DHCP/DNS 就绪）")
				return nil
			}
		}
	}
	// 2) 自己起 dnsmasq
	dnsmasq := lookBin("dnsmasq")
	if dnsmasq == "" {
		return errors.New("系统未安装 dnsmasq")
	}
	start, end, mask := dhcpRange(gateway)
	conf := filepath.Join(appDataDir, "lxc-dnsmasq.conf")
	body := strings.Join([]string{
		"# 由 LXC 管理面板生成：为容器网桥提供 DHCP/DNS",
		"interface=" + bridge,
		"bind-interfaces",
		"except-interface=lo",
		"dhcp-range=" + start + "," + end + "," + mask + ",12h",
		"dhcp-option=option:router," + gateway,
		"dhcp-option=option:dns-server," + gateway,
		"no-resolv",
		"server=" + strings.Join(dnsFallback, "\nserver="),
		"cache-size=1000",
		"log-facility=" + filepath.Join(appDataDir, "lxc-dnsmasq.log"),
		"pid-file=/run/lxc-panel-dnsmasq.pid",
		"",
	}, "\n")
	if err := os.MkdirAll(appDataDir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %v", appDataDir, err)
	}
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		return fmt.Errorf("写入 dnsmasq 配置失败: %v", err)
	}
	out, err := runCmd(15*time.Second, dnsmasq, "--conf-file="+conf)
	if err != nil {
		return fmt.Errorf("启动 dnsmasq 失败: %s", strings.TrimSpace(out))
	}
	time.Sleep(1200 * time.Millisecond)
	if !dhcpServing(bridge, gateway) {
		return fmt.Errorf("dnsmasq 已启动但没有在 %s 上监听: %s", bridge, strings.TrimSpace(out))
	}
	logf(fmt.Sprintf("已启动面板自带 dnsmasq（地址池 %s - %s，DNS %s）", start, end, gateway))
	return nil
}

// freeIPv4 在网段里挑一个没被任何容器占用的地址（从 .150 起，避开 DHCP 池前段）。
func freeIPv4(gateway string) string {
	ip := net.ParseIP(gateway).To4()
	if ip == nil {
		return ""
	}
	base := fmt.Sprintf("%d.%d.%d.", ip[0], ip[1], ip[2])
	used := map[string]bool{gateway: true}
	if names, err := lxcListNames(); err == nil {
		for _, n := range names {
			for _, a := range readNetConfig(n).IPv4Cfg {
				if v, _, err := net.ParseCIDR(a); err == nil {
					used[v.String()] = true
				} else {
					used[a] = true
				}
			}
		}
	}
	for i := 150; i < 254; i++ {
		addr := base + strconv.Itoa(i)
		if !used[addr] {
			return addr
		}
	}
	return ""
}

// assignStaticIPv4 给容器写死地址和网关，绕开「没有 DHCP」的死路。
func assignStaticIPv4(name, gateway string) (string, error) {
	nc := readNetConfig(name)
	if nc.Disabled {
		return "", errors.New("容器网络已关闭")
	}
	if len(nc.IPv4Cfg) > 0 {
		return nc.IPv4Cfg[0], nil
	}
	addr := freeIPv4(gateway)
	if addr == "" {
		return "", errors.New("网段里没有空闲地址了")
	}
	if err := setConfigKey(name, "lxc.net.0.ipv4.address", addr+"/24"); err != nil {
		return "", err
	}
	if err := setConfigKey(name, "lxc.net.0.ipv4.gateway", gateway); err != nil {
		return "", err
	}
	return addr + "/24", nil
}

// ensureContainerDNS 容器里的 resolv.conf 没有 nameserver 时补上公共 DNS。
// 只补不覆盖；resolv.conf 是软链接（systemd-resolved）时不动，避免写坏。
func ensureContainerDNS(name string) string {
	c, _ := lxcInfo(name)
	if c == nil || c.State != "RUNNING" {
		return ""
	}
	script := "if grep -q nameserver /etc/resolv.conf 2>/dev/null; then echo ok; exit 0; fi; " +
		"if [ -L /etc/resolv.conf ]; then echo symlink; exit 0; fi; " +
		fmt.Sprintf("printf 'nameserver %%s\\nnameserver %%s\\n' %s %s > /etc/resolv.conf && echo written",
			dnsFallback[0], dnsFallback[1])
	out, err := attachRun(name, script, "", 12*time.Second)
	if err != nil {
		return "写入 DNS 失败: " + strings.TrimSpace(out)
	}
	if strings.Contains(out, "written") {
		return "已为容器写入 DNS " + strings.Join(dnsFallback, " / ")
	}
	return ""
}

// hostNetFix 一次性把宿主机侧的网络补齐。返回执行过的动作列表。
func hostNetFix(logf func(string)) ([]string, error) {
	var logs []string
	add := func(s string) {
		logs = append(logs, s)
		if logf != nil {
			logf(s)
		}
	}
	if lookBin("ip") == "" {
		return logs, errors.New("系统缺少 ip 命令（iproute2），无法配置容器网络")
	}
	bridge, gateway, subnet := hostNetInfo()

	if changed, err := ensureForward(false); err != nil {
		add("开启 IPv4 转发失败: " + err.Error())
	} else if changed {
		add("已开启 net.ipv4.ip_forward=1")
	}
	if changed, err := ensureForward(true); err == nil && changed {
		add("已开启 net.ipv6.conf.all.forwarding=1")
	}
	if err := ensureBridge(bridge, gateway, add); err != nil {
		return logs, err
	}
	if !hasIptables() {
		add("警告：系统没有 iptables，无法配置 NAT，容器无法访问外网")
	} else {
		if changed, err := ensureNAT(subnet); err != nil {
			add(err.Error())
		} else if changed {
			add("已添加 NAT 出网规则 " + subnet + " → MASQUERADE")
		}
		if changed, err := ensureForwardRule(bridge); err != nil {
			add(err.Error())
		} else if changed {
			add("已放行 FORWARD 上的 " + bridge + " 流量")
		}
		// 上行 MTU 偏小时（PPPoE 1492 / WireGuard 1420），不钳 MSS 会出现
		// 「ping 通、http 通，但 https 握手卡死」这种迷惑现象。
		if changed, note, err := ensureMSSClamp(bridge); err != nil {
			add(err.Error())
		} else if changed {
			add("已添加 MSS 钳制规则（" + note + "），修复 https 握手卡住")
		}
	}

	noDHCP := false
	if err := ensureDHCP(bridge, gateway, add); err != nil {
		noDHCP = true
		add("DHCP/DNS 未就绪: " + err.Error() + "，改用静态 IP 方案")
	}

	// 老容器兜底：之前创建的容器可能既没 DHCP 也没静态地址
	if names, err := lxcListNames(); err == nil {
		for _, n := range names {
			nc := readNetConfig(n)
			if nc.Disabled || nc.Bridge == "" || nc.Bridge != bridge {
				continue
			}
			if len(nc.IPv4Cfg) == 0 && noDHCP {
				if addr, err := assignStaticIPv4(n, gateway); err != nil {
					add("容器 " + n + " 分配静态 IP 失败: " + err.Error())
				} else {
					add("容器 " + n + " 已分配静态 IP " + addr + "（网关 " + gateway + "，重启生效）")
				}
			}
			if msg := ensureContainerDNS(n); msg != "" {
				add("容器 " + n + "：" + msg)
			}
		}
	}
	if len(logs) == 0 {
		add("宿主机网络已正常，没有需要修复的项")
	}
	return logs, nil
}

// ---------- 容器内诊断 ----------

type NetProbe struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Hint   string `json:"hint,omitempty"`
}

// probeContainerNet 在容器里跑一串检查，定位到底断在哪一层。
// proxy 是面板里配的容器代理（可为空），用来判断 HTTPS 失败是不是「没走代理」。
func probeContainerNet(name, proxy string) ([]NetProbe, error) {
	c, err := lxcInfo(name)
	if err != nil {
		return nil, err
	}
	if c.State != "RUNNING" {
		return nil, errors.New("容器未运行，先启动容器再诊断")
	}
	_, gateway, _ := hostNetInfo()
	probes := []NetProbe{}

	run := func(key, label, script string, timeout time.Duration, okHint string) NetProbe {
		out, err := attachRun(name, script, "", timeout)
		p := NetProbe{Key: key, Label: label, Output: strings.TrimSpace(out)}
		p.OK = err == nil
		if !p.OK {
			p.Hint = okHint
		}
		if p.Output == "" {
			p.Output = "（无输出）"
		}
		return p
	}

	// runMatch 用于「命令本身成功不代表结果 OK」的探测：脚本一律 exit 0，
	// 由 match 判断输出里有没有成功标志。
	runMatch := func(key, label, script string, timeout time.Duration, okHint string, match func(string) bool) NetProbe {
		out, err := attachRun(name, script, "", timeout)
		p := NetProbe{Key: key, Label: label, Output: strings.TrimSpace(out)}
		if err != nil && p.Output == "" {
			p.Output = err.Error()
		}
		p.OK = match(p.Output)
		if !p.OK {
			p.Hint = okHint
		}
		if p.Output == "" {
			p.Output = "（无输出）"
		}
		return p
	}

	probes = append(probes, run("addr", "网卡与地址", "ip -4 addr show | grep -E '^[0-9]+:|inet '", 8*time.Second,
		"容器里没有 IPv4 地址：宿主机可能没有 DHCP，按下面「一键修复」处理"))
	probes = append(probes, run("route", "默认路由", "ip route show", 8*time.Second,
		"没有默认路由，容器不知道往哪发出公网流量"))
	probes = append(probes, run("dns", "DNS 配置", "cat /etc/resolv.conf 2>/dev/null | head -10", 8*time.Second,
		"resolv.conf 读不到，域名解析会失败（ping IP 不受影响）"))
	probes = append(probes, run("gw", "网关连通 ("+gateway+")", "ping -c2 -W2 "+gateway+" 2>&1 | tail -3", 15*time.Second,
		"网关不通：宿主机桥接没配地址，或容器网卡没接上桥"))
	probes = append(probes, run("inet", "公网 IP 连通 (223.5.5.5)", "ping -c2 -W2 223.5.5.5 2>&1 | tail -3", 15*time.Second,
		"网关通但公网不通：宿主机缺 ip_forward / MASQUERADE（点「一键修复」）"))
	probes = append(probes, run("resolve", "域名解析 (deb.debian.org)", "getent hosts deb.debian.org 2>&1 || nslookup deb.debian.org 2>&1 | tail -4", 20*time.Second,
		"域名解析失败：DNS 服务器不可达，检查 resolv.conf"))

	// HTTPS 才是真正的分水岭：ping 和 http 都通、但 https 断的情况极常见
	// （国际站点 TLS 握手被重置 / 上行 MTU 导致的 MSS 黑洞）。
	httpProbe := func(url string) string {
		return `if command -v curl >/dev/null 2>&1; then
  curl -sS -I --max-time 10 -o /dev/null -w '%{http_code}' ` + url + ` 2>&1
elif command -v wget >/dev/null 2>&1; then
  wget -S --spider --timeout=10 --tries=1 ` + url + ` 2>&1 | grep -m1 -oE 'HTTP/[0-9.]+ [0-9]+'
else
  echo '容器里没有 curl / wget，装一个：apt-get install -y curl'
fi
exit 0`
	}
	okHTTP := func(s string) bool {
		return strings.Contains(s, " 200") || strings.HasPrefix(s, "200") ||
			strings.Contains(s, "301") || strings.Contains(s, "302") || strings.Contains(s, "403")
	}

	probes = append(probes, runMatch("https_cn", "HTTPS 国内站点 (baidu.com)", httpProbe("https://www.baidu.com"), 25*time.Second,
		"连国内站点的 HTTPS 都握手失败：既可能是宿主机上行 MTU 偏小（PPPoE / VPN）导致 TLS 证书包被丢，"+
			"也可能是宿主机防火墙拦了 443。点「一键修复」会顺手加 MSS 钳制；仍不行请在宿主机上抓包确认。", okHTTP))
	probes = append(probes, runMatch("https_gh", "HTTPS 国际站点 (raw.githubusercontent.com)", httpProbe("https://raw.githubusercontent.com"), 25*time.Second,
		"ping 通、HTTP 通但这里失败：国内网络访问 GitHub 的 TLS 握手会被重置，这不是容器的问题。"+
			"在「运行环境 → 容器出网」填一个代理（如 http://10.0.3.1:7890）即可。", okHTTP))

	// 代理是否已经落进容器
	proxies := `for f in /etc/environment /etc/apt/apt.conf.d/99lxcpanel-proxy; do
  [ -f "$f" ] && grep -i -m3 'proxy' "$f" | sed "s|^|$(basename $f): |"
done
[ -f /etc/environment ] || echo '容器里没有 /etc/environment'
exit 0`
	if strings.TrimSpace(proxy) == "" {
		probes = append(probes, NetProbe{
			Key: "proxy", Label: "容器代理", OK: false,
			Output: "面板里没有配置容器代理",
			Hint:   "国内环境建议配一个：宿主机跑 mihomo 时填 http://10.0.3.1:7890（记得 allow-lan: true，让代理监听在网关上）",
		})
	} else {
		probes = append(probes, runMatch("proxy", "容器代理 ("+proxy+")", proxies, 12*time.Second,
			"代理还没写入容器：容器启动后会自动写；也可以点上面「应用代理」立即写一次", func(s string) bool {
				return strings.Contains(s, proxy)
			}))
	}

	return probes, nil
}
