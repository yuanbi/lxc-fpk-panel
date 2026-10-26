package main

import "testing"

func TestDHCPRange(t *testing.T) {
	start, end, mask := dhcpRange("10.0.3.1")
	if start != "10.0.3.100" || end != "10.0.3.240" || mask != "255.255.255.0" {
		t.Fatalf("默认网段地址池不对: %s-%s %s", start, end, mask)
	}
	start, end, _ = dhcpRange("192.168.50.1")
	if start != "192.168.50.100" || end != "192.168.50.240" {
		t.Fatalf("自定义网段地址池不对: %s-%s", start, end)
	}
	// 非法输入退回 lxc 默认网段，不能 panic
	start, _, _ = dhcpRange("not-an-ip")
	if start != "10.0.3.100" {
		t.Fatalf("非法网关应回落到默认池，实际 %s", start)
	}
}

func TestIPv4CIDROf(t *testing.T) {
	if got := ipv4CIDROf("10.0.3.1"); got != "10.0.3.1/24" {
		t.Fatalf("默认网关应得到 10.0.3.1/24，实际 %s", got)
	}
	if got := ipv4CIDROf("192.168.88.1"); got != "192.168.88.1/24" {
		t.Fatalf("自定义网关应得到 192.168.88.1/24，实际 %s", got)
	}
}

func TestHostNetStatusShape(t *testing.T) {
	// 这个环境没有 ip/iptables，检查项会全部判失败，但结构必须完整可序列化。
	st := hostNetStatus()
	if st.Bridge == "" {
		t.Fatal("网桥名不能为空")
	}
	if st.Gateway == "" || st.Subnet == "" {
		t.Fatalf("网关/网段不能为空: %+v", st)
	}
	if len(st.Checks) < 5 {
		t.Fatalf("检查项太少: %d", len(st.Checks))
	}
	keys := map[string]bool{}
	for _, c := range st.Checks {
		if c.Key == "" || c.Label == "" {
			t.Fatalf("检查项缺少 key/label: %+v", c)
		}
		keys[c.Key] = true
	}
	for _, want := range []string{"bridge", "forward", "nat", "fwd", "dhcp"} {
		if !keys[want] {
			t.Fatalf("缺少检查项 %s", want)
		}
	}
}
