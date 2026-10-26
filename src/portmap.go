package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 端口转发：把宿主机（飞牛）上的端口映射到容器里的服务。
//
// 实现方式是内核 DNAT：宿主机 PREROUTING 命中「宿主端口」后改写目标地址为
// 容器 IP + 容器端口，再由 FORWARD 放行。规则放在自建链 LXCPANEL 里，
// 每次同步先清空再按存档重建——容器 DHCP 换 IP、面板重启都能自动对齐，
// 也不用去猜哪条规则是哪个容器的。
//
// 规则本身存 ${TRIG_PKGVAR}/portmap.json，容器启动、面板启动、手动刷新时重建。

type PortRule struct {
	ID         string `json:"id"`
	Container  string `json:"container"`
	Proto      string `json:"proto"`
	HostPort   int    `json:"host_port"`
	TargetPort int    `json:"target_port"`
	Note       string `json:"note,omitempty"`
	CreatedAt  int64  `json:"created_at"`

	// 以下为展示用，随容器当前状态填充，不落盘
	TargetIP string `json:"target_ip,omitempty"`
	TargetV6 string `json:"target_v6,omitempty"`
	State    string `json:"state,omitempty"`
	// TargetOK 表示从宿主机直连容器目标端口是否成功（只有 TCP 会探测）。
	TargetOK bool   `json:"target_ok"`
	Reach    string `json:"reach,omitempty"`
}

type PortStore struct {
	mu   sync.RWMutex
	path string
	data []PortRule
}

func NewPortStore(path string) *PortStore {
	s := &PortStore{path: path}
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, &s.data)
	}
	return s
}

func (s *PortStore) List() []PortRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PortRule, len(s.data))
	copy(out, s.data)
	return out
}

func (s *PortStore) Get(id string) (PortRule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.data {
		if r.ID == id {
			return r, true
		}
	}
	return PortRule{}, false
}

func (s *PortStore) Add(r PortRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.data {
		if e.Proto == r.Proto && e.HostPort == r.HostPort {
			return fmt.Errorf("宿主端口 %d/%s 已被容器 %s 占用", r.HostPort, r.Proto, e.Container)
		}
	}
	if r.ID == "" {
		r.ID = newRuleID()
	}
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().Unix()
	}
	s.data = append(s.data, r)
	return s.saveLocked()
}

func (s *PortStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.data {
		if r.ID == id {
			s.data = append(s.data[:i], s.data[i+1:]...)
			s.saveLocked()
			return true
		}
	}
	return false
}

func (s *PortStore) saveLocked() error {
	raw, _ := json.MarshalIndent(s.data, "", "  ")
	return os.WriteFile(s.path, raw, 0o644)
}

func newRuleID() string {
	var b [4]byte
	rand.Read(b[:])
	return "pf-" + hex.EncodeToString(b[:])
}

// ---------- iptables ----------

// 链名：nat / filter 各一条；OUTPUT 单独一条，因为宿主机自己访问不会经过 PREROUTING。
const (
	portNatChain = "LXCPANEL"
	portOutChain = "LXCPANEL-OUT"
	portFwdChain = "LXCPANEL"
)

// containerAddr 取容器第一个可用的 IPv4 / IPv6 地址。
// IPv6 跳过 fe80:: 链路本地地址——它不能跨主机路由，拿来做 DNAT 必然不通。
func containerAddr(name string) (v4, v6 string) {
	c, err := lxcInfo(name)
	if err != nil || c == nil {
		return "", ""
	}
	for _, ip := range c.IPv4 {
		if n := net.ParseIP(ip); n != nil && n.To4() != nil {
			v4 = ip
			break
		}
	}
	for _, ip := range c.IPv6 {
		n := net.ParseIP(ip)
		if n == nil || n.To4() != nil || n.IsLinkLocalUnicast() || n.IsLoopback() {
			continue
		}
		v6 = ip
		break
	}
	return v4, v6
}

// fwCmd 是一条待写入的规则。table 取 nat / filter / out，
// out 表示写进 nat 表的 LXCPANEL-OUT（OUTPUT 链），专门给宿主机本机访问用。
type fwCmd struct {
	table string
	args  string
}

// portRules 为一条转发规则生成三条相同的改写逻辑。
//
// 关键点是 DNAT 不指定 -d，而是用 `-m addrtype --dst-type LOCAL`：这样只要目标是
// 宿主机上的**任意**地址都会被命中——0.0.0.0 意义上的所有 IPv4、局域网 IP、公网 IPv4，
// 以及宿主机上的所有 IPv6（公网 / ULA / 临时地址）。写死某个 -d 就只能从一个地址进。
func portRules(r PortRule, v4, v6 string) (r4, r6 []fwCmd) {
	wrap := func(ip string) string {
		if strings.Contains(ip, ":") {
			return "[" + ip + "]"
		}
		return ip
	}
	build := func(ip string) []fwCmd {
		nat := fmt.Sprintf("-m addrtype --dst-type LOCAL -p %s -m %s --dport %d -j DNAT --to-destination %s:%d",
			r.Proto, r.Proto, r.HostPort, wrap(ip), r.TargetPort)
		// 去往容器的新连接放行；ESTABLISHED 一起带上，回程包（源是容器）也能命中
		fwd := fmt.Sprintf("-p %s -d %s -m %s --dport %d -m conntrack --ctstate NEW,ESTABLISHED -j ACCEPT",
			r.Proto, ip, r.Proto, r.TargetPort)
		return []fwCmd{
			{"nat", nat},    // 外部经宿主网卡进来
			{"out", nat},    // 宿主机本机 curl 127.0.0.1 / 宿主 IP
			{"filter", fwd}, // FORWARD 放行
		}
	}
	if v4 != "" {
		r4 = build(v4)
	}
	if v6 != "" {
		r6 = build(v6)
	}
	return
}

// ensureJump 保证 parent 链的**第一条**规则就是跳到 chain。
//
// 只判断「有没有挂上」不够：Docker 之类的服务重启时会把自己插到 FORWARD 最前面，
// 我们的链被挤到后面之后，它前面的 DROP 会先命中，转发就断了。
// 所以每次同步都对着 -S 的输出核对一遍首位，不对就删掉重插到 1 号位。
func ensureJump(bin, table, parent, chain string) error {
	if out, err := runCmd(6*time.Second, bin, "-t", table, "-S", parent); err == nil {
		if chainHeadIsJump(out, parent, chain) {
			return nil
		}
	}
	// 旧位置上的那条要先删掉，否则会同时挂两条
	runCmd(6*time.Second, bin, "-t", table, "-D", parent, "-j", chain)
	if out, err := runCmd(6*time.Second, bin, "-t", table, "-I", parent, "1", "-j", chain); err != nil {
		return fmt.Errorf("把 %s 挂到 %s/%s 失败: %s", chain, table, parent, strings.TrimSpace(out))
	}
	return nil
}

// chainHeadIsJump 从 `iptables -S <parent>` 的输出判断第一条规则是不是跳到 chain。
// 必须按 token 精确比较：`-j LXCPANEL` 是 `-j LXCPANEL-OUT` 的前缀，用 Contains 会误判。
func chainHeadIsJump(out, parent, chain string) bool {
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(strings.TrimSpace(ln))
		if len(f) < 2 || f[0] != "-A" || f[1] != parent {
			continue
		}
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "-j" {
				return f[i+1] == chain
			}
		}
		return false
	}
	return false
}

// ensureChain 保证链存在、已清空，并挂在 parent 链首（直接跳转，压过系统的默认 DROP）。
func ensureChain(bin, table, chain, parent string) error {
	runCmd(6*time.Second, bin, "-t", table, "-N", chain)
	if _, err := runCmd(6*time.Second, bin, "-t", table, "-F", chain); err != nil {
		return fmt.Errorf("%s 的 %s 表不可用: %v", bin, table, err)
	}
	return ensureJump(bin, table, parent, chain)
}

// appendRules 把 cmds 逐条追加到对应的链里。
func appendRules(bin string, cmds []fwCmd) error {
	for _, c := range cmds {
		table, chain := c.table, portNatChain
		switch c.table {
		case "out":
			table, chain = "nat", portOutChain
		case "filter":
			chain = portFwdChain
		}
		args := append([]string{"-t", table, "-A", chain}, strings.Fields(c.args)...)
		if out, err := runCmd(8*time.Second, bin, args...); err != nil {
			return fmt.Errorf("添加规则失败: %s", strings.TrimSpace(out))
		}
	}
	return nil
}

// ensureHairpin 补 NAT 回环（hairpin）：
//
// DNAT 之后容器看到的源地址是真正的客户端。容器回包时源是自己的地址，
// 直接回给客户端——客户端发现源地址不是自己连的那台主机，会丢弃，
// 表现就是「端口转发点了没反应 / 连接超时」。
//
// 两种情况要在出桥时把源改写掉：
//  1. 从宿主机自己访问（源是宿主地址，含 127.0.0.1 DNAT 后选出的网关地址）；
//  2. 同网段容器互访（源和目标都在桥网段，普通 MASQUERADE 的 ! -d 条件不命中）。
func ensureHairpin(bin, bridge, subnet string) (bool, error) {
	if bin == "" || bridge == "" || subnet == "" {
		return false, nil
	}
	specs := [][]string{
		{"-t", "nat", "-A", "POSTROUTING", "-o", bridge, "-m", "addrtype", "--src-type", "LOCAL", "-j", "MASQUERADE"},
		{"-t", "nat", "-A", "POSTROUTING", "-o", bridge, "-s", subnet, "-d", subnet, "-j", "MASQUERADE"},
	}
	changed := false
	for _, spec := range specs {
		chk := append([]string{}, spec...)
		chk[2] = "-C"
		if _, err := runCmd(6*time.Second, bin, chk...); err == nil {
			continue
		}
		if out, err := runCmd(6*time.Second, bin, spec...); err != nil {
			return changed, errors.New(strings.TrimSpace(out))
		}
		changed = true
	}
	return changed, nil
}

// ensureV6Masq 让 ULA 地址的容器能从宿主机 IPv6 出去。
// 容器只有 fd42::/64 私有地址，直接回包给公网客户端是回不去的。
func ensureV6Masq(bin, bridge, subnet string) (bool, error) {
	if bin == "" || bridge == "" || subnet == "" {
		return false, nil
	}
	spec := []string{"-t", "nat", "-A", "POSTROUTING", "-o", bridge, "-s", subnet, "!", "-d", subnet, "-j", "MASQUERADE"}
	chk := append([]string{}, spec...)
	chk[2] = "-C"
	if _, err := runCmd(6*time.Second, bin, chk...); err == nil {
		return false, nil
	}
	if out, err := runCmd(6*time.Second, bin, spec...); err != nil {
		return false, errors.New(strings.TrimSpace(out))
	}
	return true, nil
}

// syncPorts 按存档重建全部 DNAT 规则，并把结果写回每条规则的运行状态。
func (a *App) syncPorts(logf func(string)) error {
	log := func(s string) {
		if logf != nil {
			logf(s)
		}
	}
	bin4 := lookBin("iptables")
	if bin4 == "" {
		return errors.New("系统里找不到 iptables，无法做端口转发")
	}
	bin6 := lookBin("ip6tables")

	rules := a.ports.List()
	// 先解析各容器的当前地址（同一容器只查一次）
	cache := map[string][2]string{}
	for _, r := range rules {
		if _, ok := cache[r.Container]; ok {
			continue
		}
		v4, v6 := containerAddr(r.Container)
		cache[r.Container] = [2]string{v4, v6}
	}

	var v4cmds, v6cmds []fwCmd
	v4n, v6n := 0, 0
	for _, r := range rules {
		ad := cache[r.Container]
		c4, c6 := portRules(r, ad[0], ad[1])
		if len(c4) == 0 && len(c6) == 0 {
			log(fmt.Sprintf("跳过 %d/%s → %s（容器未运行或没有 IP）", r.HostPort, r.Proto, r.Container))
			continue
		}
		if len(c4) > 0 {
			v4n++
		}
		if len(c6) > 0 {
			v6n++
		}
		v4cmds = append(v4cmds, c4...)
		v6cmds = append(v6cmds, c6...)
	}

	apply := func(bin string, cmds []fwCmd) error {
		for _, c := range []struct{ table, chain, parent string }{
			{"nat", portNatChain, "PREROUTING"},
			{"nat", portOutChain, "OUTPUT"},
			{"filter", portFwdChain, "FORWARD"},
		} {
			if err := ensureChain(bin, c.table, c.chain, c.parent); err != nil {
				return err
			}
		}
		return appendRules(bin, cmds)
	}

	var firstErr error
	if err := apply(bin4, v4cmds); err != nil {
		firstErr = err
		log("IPv4 端口转发失败: " + err.Error())
	} else {
		log(fmt.Sprintf("IPv4 转发已同步（%d 条规则，PREROUTING + OUTPUT + FORWARD）", v4n))
	}

	if bin6 == "" {
		if v6n > 0 {
			log("容器有 IPv6 地址，但系统缺少 ip6tables，IPv6 转发已跳过")
		}
	} else if err := apply(bin6, v6cmds); err != nil {
		if firstErr == nil {
			firstErr = err
		}
		log("IPv6 端口转发失败: " + err.Error())
	} else if v6n > 0 {
		log(fmt.Sprintf("IPv6 转发已同步（%d 条规则）", v6n))
	}

	// 转发开关是 DNAT 的前提，顺手保证一下（幂等，不会重复改）
	if changed, err := ensureForward(false); err != nil {
		log("开启 IPv4 转发失败: " + err.Error())
	} else if changed {
		log("已开启 net.ipv4.ip_forward")
	}
	if changed, err := ensureForward(true); err != nil {
		log("开启 IPv6 转发失败: " + err.Error())
	} else if changed {
		log("已开启 net.ipv6.conf.all.forwarding")
	}

	bridge, _, subnet := hostNetInfo()
	if changed, err := ensureHairpin(bin4, bridge, subnet); err != nil {
		log("补齐 NAT 回环失败: " + err.Error())
	} else if changed {
		log("已补齐 NAT 回环（宿主机本机 / 容器互访访问宿主端口）")
	}
	if bin6 != "" {
		if changed, err := ensureV6Masq(bin6, bridge, lxcULAPrefix+"::/64"); err != nil {
			log("补齐 IPv6 出网改写失败: " + err.Error())
		} else if changed {
			log("已补齐 IPv6 出网改写（" + lxcULAPrefix + "::/64 → 宿主 IPv6）")
		}
	}
	return firstErr
}

// probeTarget 从宿主机直连容器端口，判断容器里的服务是不是真的在听。
// 转发规则正确但服务没起，表现和「转发没生效」一模一样，分开提示能省很多来回。
func probeTarget(ip string, port int, proto string) (bool, string) {
	if ip == "" {
		return false, "容器没有可用 IP"
	}
	if proto == "udp" {
		return false, "UDP 无法探测"
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 700*time.Millisecond)
	if err != nil {
		return false, "容器里 " + addr + " 没有监听（服务没起来？）"
	}
	conn.Close()
	return true, "容器端口可达"
}

// ruleView 给每条规则补上目标地址与状态，供列表展示。
func (a *App) ruleViews() []PortRule {
	rules := a.ports.List()
	cache := map[string][2]string{}
	for i := range rules {
		name := rules[i].Container
		if _, ok := cache[name]; !ok {
			v4, v6 := containerAddr(name)
			cache[name] = [2]string{v4, v6}
		}
		ad := cache[name]
		rules[i].TargetIP, rules[i].TargetV6 = ad[0], ad[1]
		dst := ad[0]
		if dst == "" {
			dst = ad[1]
		}
		switch {
		case dst == "":
			rules[i].State = "容器未运行"
			rules[i].Reach = "容器没有 IP，转发规则已跳过"
		case !hasIptables():
			rules[i].State = "缺少 iptables"
			rules[i].Reach = "系统里找不到 iptables"
		default:
			rules[i].State = "已启用"
		}
	}

	// 并发探测容器端口，避免规则多了把列表拖慢
	var wg sync.WaitGroup
	for i := range rules {
		if rules[i].State != "已启用" {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ip := rules[i].TargetIP
			if ip == "" {
				ip = rules[i].TargetV6
			}
			ok, detail := probeTarget(ip, rules[i].TargetPort, rules[i].Proto)
			rules[i].TargetOK = ok
			rules[i].Reach = detail
		}(i)
	}
	wg.Wait()
	return rules
}

type portMapStatus struct {
	Iptables    bool     `json:"iptables"`
	Ip6tables   bool     `json:"ip6tables"`
	IPv4Forward bool     `json:"ipv4_forward"`
	IPv6Forward bool     `json:"ipv6_forward"`
	HostIPv4    []string `json:"host_ipv4"`
	HostIPv6    []string `json:"host_ipv6"`
	Bridge      string   `json:"bridge"`
	// OutChain 表示 nat/OUTPUT 里的 LXCPANEL-OUT 是否就位（宿主机本机访问要靠它）
	OutChain bool `json:"out_chain"`
	// Hairpin 表示 NAT 回环是否就位
	Hairpin bool   `json:"hairpin"`
	Error   string `json:"error"`
}

func currentPortStatus() portMapStatus {
	bin4, bin6 := lookBin("iptables"), lookBin("ip6tables")
	bridge, _, _ := hostNetInfo()
	st := portMapStatus{
		Iptables:    bin4 != "",
		Ip6tables:   bin6 != "",
		IPv4Forward: ipv4ForwardOn(),
		IPv6Forward: ipv6ForwardOn(),
		HostIPv4:    []string{},
		HostIPv6:    []string{},
		Bridge:      bridge,
	}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
				continue
			}
			addrs, _ := ifc.Addrs()
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				if ipnet.IP.To4() != nil {
					st.HostIPv4 = append(st.HostIPv4, ipnet.IP.String())
				} else if !ipnet.IP.IsLinkLocalUnicast() {
					st.HostIPv6 = append(st.HostIPv6, ipnet.IP.String())
				}
			}
		}
	}
	if bin4 != "" {
		if _, err := runCmd(5*time.Second, bin4, "-t", "nat", "-C", "OUTPUT", "-j", portOutChain); err == nil {
			st.OutChain = true
		}
		if _, err := runCmd(5*time.Second, bin4, "-t", "nat", "-C", "POSTROUTING", "-o", bridge,
			"-m", "addrtype", "--src-type", "LOCAL", "-j", "MASQUERADE"); err == nil {
			st.Hairpin = true
		}
	}
	if bin4 == "" {
		st.Error = "系统里找不到 iptables，端口转发不可用"
	}
	return st
}

// hostPortListening 检查宿主机上是否已有进程监听该端口。
// PREROUTING 的 DNAT 会把发往宿主该端口的流量也劫走，先拦下来比事后排查省事。
func hostPortListening(proto string, port int) bool {
	ss := lookBin("ss")
	if ss == "" {
		return false
	}
	flag := "-ltn"
	if proto == "udp" {
		flag = "-lun"
	}
	out, err := runCmd(5*time.Second, ss, "-H", flag)
	if err != nil {
		return false
	}
	want := strconv.Itoa(port)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		local := f[3]
		if i := strings.LastIndex(local, ":"); i >= 0 && local[i+1:] == want {
			return true
		}
	}
	return false
}

// PortProbe 是一条转发规则的分层连通性检测结果。
type PortProbe struct {
	Scope  string `json:"scope"`  // container / loopback / host / host6
	Addr   string `json:"addr"`   // 实际探测的 host:port
	Label  string `json:"label"`  // 给人看的说明
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// probeRule 在宿主机上分层探测一条转发规则：
//
//	container  → 宿主机直连容器 IP:端口（服务到底有没有起来）
//	loopback   → 127.0.0.1:宿主端口（OUTPUT 链 + NAT 回环有没有生效）
//	host/host6 → 宿主机每个网卡地址:宿主端口（外部客户端走的同一条 DNAT 路径）
func probeRule(r PortRule) []PortProbe {
	var out []PortProbe
	v4, v6 := containerAddr(r.Container)
	target := v4
	if target == "" {
		target = v6
	}
	if target != "" {
		ok, msg := probeTarget(target, r.TargetPort, r.Proto)
		out = append(out, PortProbe{
			Scope: "container", Addr: net.JoinHostPort(target, strconv.Itoa(r.TargetPort)),
			Label: "宿主机 → 容器 " + target + ":" + strconv.Itoa(r.TargetPort), OK: ok, Detail: msg,
		})
	} else {
		out = append(out, PortProbe{
			Scope: "container", Label: "宿主机 → 容器", OK: false,
			Detail: "容器未运行或没有 IP，规则已跳过",
		})
	}
	if r.Proto == "udp" {
		out = append(out, PortProbe{Scope: "skip", Label: "宿主机 → 宿主端口", OK: false, Detail: "UDP 无法探测，请在容器里用 nc -u 试"})
		return out
	}

	st := currentPortStatus()
	dial := func(scope, label, ip string) {
		addr := net.JoinHostPort(ip, strconv.Itoa(r.HostPort))
		conn, err := net.DialTimeout("tcp", addr, 900*time.Millisecond)
		ok := err == nil
		detail := "端口通"
		if !ok {
			detail = "连接失败（" + strings.TrimSpace(err.Error()) + "）"
		} else {
			conn.Close()
		}
		out = append(out, PortProbe{Scope: scope, Addr: addr, Label: label, OK: ok, Detail: detail})
	}
	dial("loopback", "宿主机 → 127.0.0.1:"+strconv.Itoa(r.HostPort)+"（本机访问，走 OUTPUT 链）", "127.0.0.1")
	for _, ip := range st.HostIPv4 {
		dial("host", "宿主机 → "+ip+":"+strconv.Itoa(r.HostPort)+"（外部客户端走同一条路径）", ip)
	}
	for _, ip := range st.HostIPv6 {
		dial("host6", "宿主机 → ["+ip+"]:"+strconv.Itoa(r.HostPort)+"（IPv6）", ip)
	}
	return out
}

// ---------- HTTP ----------

func (a *App) handlePortMap(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"rules": a.ruleViews(), "status": currentPortStatus()})
	case http.MethodPost:
		var body struct {
			Container  string `json:"container"`
			Proto      string `json:"proto"`
			HostPort   int    `json:"host_port"`
			TargetPort int    `json:"target_port"`
			Note       string `json:"note"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			writeErr(w, 400, "请求格式错误")
			return
		}
		if !validName(body.Container) {
			writeErr(w, 400, "请选择容器")
			return
		}
		if _, err := os.Stat(filepath.Join(lxcPath(), body.Container, "config")); err != nil {
			writeErr(w, 404, "容器不存在")
			return
		}
		body.Proto = strings.ToLower(strings.TrimSpace(body.Proto))
		if body.Proto == "" {
			body.Proto = "tcp"
		}
		if body.Proto != "tcp" && body.Proto != "udp" {
			writeErr(w, 400, "协议只能是 tcp 或 udp")
			return
		}
		if body.HostPort < 1 || body.HostPort > 65535 || body.TargetPort < 1 || body.TargetPort > 65535 {
			writeErr(w, 400, "端口范围 1 - 65535")
			return
		}
		if hostPortListening(body.Proto, body.HostPort) {
			writeErr(w, 409, fmt.Sprintf("宿主端口 %d/%s 已被本机其它服务占用，换一个端口", body.HostPort, body.Proto))
			return
		}
		rule := PortRule{
			Container:  body.Container,
			Proto:      body.Proto,
			HostPort:   body.HostPort,
			TargetPort: body.TargetPort,
			Note:       strings.TrimSpace(body.Note),
		}
		if err := a.ports.Add(rule); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		var logs []string
		if err := a.syncPorts(func(s string) { logs = append(logs, s) }); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "logs": logs})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "logs": logs})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (a *App) handlePortMapSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/portmap/"), "/")
	if strings.HasSuffix(rest, "/probe") {
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method not allowed")
			return
		}
		id := strings.TrimSuffix(rest, "/probe")
		rule, ok := a.ports.Get(id)
		if !ok {
			writeErr(w, 404, "规则不存在")
			return
		}
		writeJSON(w, map[string]any{"probes": probeRule(rule)})
		return
	}
	if rest == "sync" {
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method not allowed")
			return
		}
		var logs []string
		err := a.syncPorts(func(s string) { logs = append(logs, s) })
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "logs": logs})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "logs": logs})
		return
	}
	if rest == "" {
		writeErr(w, 404, "not found")
		return
	}
	if r.Method != http.MethodDelete {
		writeErr(w, 405, "method not allowed")
		return
	}
	if !a.ports.Delete(rest) {
		writeErr(w, 404, "规则不存在")
		return
	}
	var logs []string
	if err := a.syncPorts(func(s string) { logs = append(logs, s) }); err != nil {
		writeJSON(w, map[string]any{"ok": true, "logs": logs, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "logs": logs})
}

// handleContainerIPv6 开启/关闭容器的内网 IPv6。
func (a *App) handleContainerIPv6(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if !body.Enable {
		if err := disableContainerIPv6(name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "logs": []string{"已移除容器的 IPv6 地址，重启后生效"}})
		return
	}
	addr, logs, err := enableContainerIPv6(name)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "logs": logs})
		return
	}
	c, _ := lxcInfo(name)
	needRestart := c != nil && c.State == "RUNNING"
	if needRestart {
		logs = append(logs, "容器正在运行，重启后 IPv6 生效")
	}
	writeJSON(w, map[string]any{"ok": true, "address": addr, "logs": logs, "need_restart": needRestart})
}
