package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortStoreConflictAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "portmap.json")
	st := NewPortStore(path)

	if err := st.Add(PortRule{Container: "web", Proto: "tcp", HostPort: 8080, TargetPort: 80}); err != nil {
		t.Fatalf("首次添加失败: %v", err)
	}
	if err := st.Add(PortRule{Container: "db", Proto: "tcp", HostPort: 8080, TargetPort: 5432}); err == nil {
		t.Fatal("同协议同宿主端口应冲突")
	}
	// 同端口不同协议是允许的
	if err := st.Add(PortRule{Container: "db", Proto: "udp", HostPort: 8080, TargetPort: 5432}); err != nil {
		t.Fatalf("udp 同端口不应冲突: %v", err)
	}

	// 重新载入应能读回并生成 id
	st2 := NewPortStore(path)
	rules := st2.List()
	if len(rules) != 2 {
		t.Fatalf("持久化后应有 2 条规则，实际 %d", len(rules))
	}
	for _, r := range rules {
		if r.ID == "" {
			t.Fatal("规则缺少 id")
		}
	}
	if !st2.Delete(rules[0].ID) {
		t.Fatal("删除失败")
	}
	if len(NewPortStore(path).List()) != 1 {
		t.Fatal("删除后应剩 1 条")
	}
	if st2.Delete("pf-not-exist") {
		t.Fatal("删除不存在的规则应返回 false")
	}
}

func TestReadNetConfig(t *testing.T) {
	dir := t.TempDir()
	setLXCPath(dir)
	t.Cleanup(func() { setLXCPath(defaultLXCStateDir) })

	mk := func(name, cfg string) {
		os.MkdirAll(filepath.Join(dir, name), 0o755)
		os.WriteFile(filepath.Join(dir, name, "config"), []byte(cfg), 0o644)
	}

	mk("web", `lxc.net.0.type = veth
lxc.net.0.link = lxcbr0
lxc.net.0.flags = up
lxc.net.0.hwaddr = 00:16:3e:aa:bb:cc
lxc.net.0.ipv6.address = fd42:6c78:63::5/64
`)
	nc := readNetConfig("web")
	if nc.Type != "veth" || nc.Bridge != "lxcbr0" || nc.MAC != "00:16:3e:aa:bb:cc" {
		t.Fatalf("解析错误: %+v", nc)
	}
	if len(nc.IPv6Cfg) != 1 || nc.IPv6Cfg[0] != "fd42:6c78:63::5/64" {
		t.Fatalf("IPv6 未解析: %+v", nc.IPv6Cfg)
	}
	if nc.Disabled {
		t.Fatal("有 veth 时不应判定为无网络")
	}

	mk("offline", `lxc.rootfs.path = dir:/var/lib/lxc/offline/rootfs
`)
	if nc := readNetConfig("offline"); !nc.Disabled {
		t.Fatalf("没有 lxc.net.* 应判定为无网络: %+v", nc)
	}

	// 关闭网络后再读应看不到 lxc.net.*
	mk("tmp", "lxc.net.0.type = veth\nlxc.net.0.link = lxcbr0\nlxc.apparmor.profile = unconfined\n")
	if err := removeNetKeys("tmp"); err != nil {
		t.Fatalf("removeNetKeys: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "tmp", "config"))
	if string(raw) != "lxc.apparmor.profile = unconfined\n" {
		t.Fatalf("只应删掉 lxc.net.* 行，实际: %q", raw)
	}
}

// 创建时的网络/磁盘参数只接受约定值，避免写进 config 变成无效行。
func TestSanitizeCreateNetwork(t *testing.T) {
	dir := t.TempDir()
	setLXCPath(dir)
	t.Cleanup(func() { setLXCPath(defaultLXCStateDir) })

	base := CreateRequest{Name: "ok1", Distro: "debian", Release: "bookworm", Arch: "amd64"}
	if err := sanitizeCreate(&base); err != nil {
		t.Fatalf("默认参数应通过: %v", err)
	}
	for _, bad := range []CreateRequest{
		{Name: "ok2", Network: "bridge"},
		{Name: "ok3", IPv6: "yes"},
	} {
		bad.Distro, bad.Release, bad.Arch = "debian", "bookworm", "amd64"
		if err := sanitizeCreate(&bad); err == nil {
			t.Errorf("非法参数应被拒绝: %+v", bad)
		}
	}
}

// 一条规则要同时生成 IPv4 / IPv6、PREROUTING / OUTPUT / FORWARD 三处，
// 而且 DNAT 必须用 addrtype LOCAL（覆盖 0.0.0.0 与宿主所有 IPv6），不能写死某个 -d。
func TestPortRulesDualStack(t *testing.T) {
	r := PortRule{Container: "web", Proto: "tcp", HostPort: 8080, TargetPort: 80}
	v4, v6 := portRules(r, "10.0.3.117", "fd42:6c78:63::2")

	if len(v4) != 3 || len(v6) != 3 {
		t.Fatalf("v4/v6 各应生成 3 条（nat/out/filter），实际 %d / %d", len(v4), len(v6))
	}

	bysTable := func(cmds []fwCmd, table string) string {
		for _, c := range cmds {
			if c.table == table {
				return c.args
			}
		}
		return ""
	}

	for _, table := range []string{"nat", "out", "filter"} {
		if bysTable(v4, table) == "" {
			t.Errorf("IPv4 缺少 %s 规则", table)
		}
		if bysTable(v6, table) == "" {
			t.Errorf("IPv6 缺少 %s 规则", table)
		}
	}

	nat4 := bysTable(v4, "nat")
	if !strings.Contains(nat4, "--dst-type LOCAL") {
		t.Errorf("DNAT 应使用 addrtype --dst-type LOCAL，实际: %s", nat4)
	}
	if strings.Contains(nat4, "-i lo") {
		t.Errorf("不应该再用 ! -i lo 的写法（会漏掉宿主机本机访问），实际: %s", nat4)
	}
	if !strings.Contains(nat4, "--to-destination 10.0.3.117:80") {
		t.Errorf("IPv4 DNAT 目标不对: %s", nat4)
	}

	// IPv6 的 --to-destination 必须是 [addr]:port，否则 ip6tables 无法解析
	nat6 := bysTable(v6, "nat")
	if !strings.Contains(nat6, "--to-destination [fd42:6c78:63::2]:80") {
		t.Errorf("IPv6 DNAT 目标应为 [addr]:port，实际: %s", nat6)
	}

	// OUTPUT 与 PREROUTING 的改写内容一致
	if bysTable(v4, "out") != nat4 {
		t.Errorf("OUTPUT 规则应与 PREROUTING 一致，实际: %s", bysTable(v4, "out"))
	}

	fwd := bysTable(v4, "filter")
	if !strings.Contains(fwd, "--ctstate NEW,ESTABLISHED") {
		t.Errorf("FORWARD 应包含 ESTABLISHED，否则回程会被默认 DROP 拦掉: %s", fwd)
	}
}

// 容器只有单栈地址时，另一栈不生成规则（避免拿空地址拼出无效规则）。
func TestPortRulesSingleStack(t *testing.T) {
	r := PortRule{Container: "web", Proto: "udp", HostPort: 5353, TargetPort: 53}

	if v4, v6 := portRules(r, "10.0.3.5", ""); len(v4) != 3 || len(v6) != 0 {
		t.Fatalf("只有 IPv4 时不应生成 IPv6 规则，实际 %d / %d", len(v4), len(v6))
	}
	if v4, v6 := portRules(r, "", "fd42:6c78:63::5"); len(v4) != 0 || len(v6) != 3 {
		t.Fatalf("只有 IPv6 时不应生成 IPv4 规则，实际 %d / %d", len(v4), len(v6))
	}
	if v4, v6 := portRules(r, "", ""); len(v4) != 0 || len(v6) != 0 {
		t.Fatal("没有地址时不应生成任何规则")
	}
	if _, v6 := portRules(r, "", "fd42:6c78:63::5"); !strings.Contains(v6[0].args, "-m udp --dport 5353") {
		t.Errorf("协议与端口应正确带入: %s", v6[0].args)
	}
}

// 链首判定要按 token 精确比：`-j LXCPANEL` 是 `-j LXCPANEL-OUT` 的前缀。
func TestChainHeadIsJump(t *testing.T) {
	onTop := `-P FORWARD ACCEPT
-A FORWARD -j LXCPANEL
-A FORWARD -j DOCKER-USER
`
	if !chainHeadIsJump(onTop, "FORWARD", "LXCPANEL") {
		t.Error("链首就是 LXCPANEL 时应判定为已就位")
	}
	// 前缀相似的链不能算命中
	if chainHeadIsJump(`-A OUTPUT -j LXCPANEL-OUT`, "OUTPUT", "LXCPANEL") {
		t.Error("LXCPANEL-OUT 不应被当成 LXCPANEL")
	}
	if !chainHeadIsJump(`-A OUTPUT -j LXCPANEL-OUT`, "OUTPUT", "LXCPANEL-OUT") {
		t.Error("LXCPANEL-OUT 自身应命中")
	}
	// 被 Docker 挤到后面：需要重插
	pushed := `-P FORWARD ACCEPT
-A FORWARD -j DOCKER-USER
-A FORWARD -j LXCPANEL
`
	if chainHeadIsJump(pushed, "FORWARD", "LXCPANEL") {
		t.Error("不在链首时应判定为未就位，需要重插到 1 号位")
	}
	if chainHeadIsJump("", "FORWARD", "LXCPANEL") {
		t.Error("空输出应判定为未就位")
	}
}
