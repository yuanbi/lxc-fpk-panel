#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""本地预览用 Mock 服务：按真实 API 契约返回假数据，用于界面视觉验证。

用法: python tools/mock_server.py [port]
"""
import json
import os
import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WWW = os.environ.get("MOCK_WWW") or os.path.join(ROOT, "src", "www")

NOW = 1759000000
CONTAINERS = [
    {
        "name": "debian-bookworm", "state": "RUNNING", "pid": 2841,
        "ipv4": ["192.168.1.24"], "ipv6": [], "mem_bytes": 268435456,
        "mem_limit": 2147483648, "cpu_seconds": 1841.2, "cpu_percent": 3.42,
        "cpu_limit": 2, "autostart": True, "arch": "amd64",
        "os": "Debian GNU/Linux 12 (bookworm)", "distro": "debian", "release": "12",
        "rootfs": "/var/lib/lxc/debian-bookworm/rootfs", "created_at": NOW - 86400 * 6,
        "tx_bytes": 18432012, "rx_bytes": 92830211, "privileged": False,
        "disk_mode": "dir", "disk_bytes": 0,
        "bridge": "lxcbr0", "veth": "veth1a2b3c", "mac": "00:16:3e:aa:01:01",
        "root_pass": "Km7tRq2WpzXa4V",
        "ipv6_cfg": [], "net_off": False,
    },
    {
        "name": "ubuntu-noble", "state": "RUNNING", "pid": 3110,
        "ipv4": ["192.168.1.31"], "ipv6": [], "mem_bytes": 1132462080,
        "mem_limit": 4294967296, "cpu_seconds": 9211.8, "cpu_percent": 12.86,
        "cpu_limit": 4, "autostart": True, "arch": "amd64",
        "os": "Ubuntu 24.04.1 LTS", "distro": "ubuntu", "release": "24.04",
        "rootfs": "/var/lib/lxc/ubuntu-noble/rootfs", "created_at": NOW - 86400 * 21,
        "tx_bytes": 923018222, "rx_bytes": 4210388221, "privileged": False,
        "disk_mode": "loop", "disk_bytes": 21474836480,
        "bridge": "lxcbr0", "veth": "veth4d5e6f", "mac": "00:16:3e:aa:02:02",
        "root_pass": "Hu9dNc5YbTe3Ls",
        "ipv6_cfg": ["fd42:6c78:63::2/64"], "ipv6": ["fd42:6c78:63::2"],
    },
    {
        "name": "alpine-edge", "state": "STOPPED", "pid": 0,
        "ipv4": [], "ipv6": [], "mem_bytes": 0, "mem_limit": 536870912,
        "cpu_seconds": 42.5, "cpu_percent": 0, "cpu_limit": 1,
        "autostart": False, "arch": "amd64", "os": "Alpine Linux v3.20",
        "distro": "alpine", "release": "3.20",
        "rootfs": "/var/lib/lxc/alpine-edge/rootfs", "created_at": NOW - 86400 * 2,
        "tx_bytes": 1024, "rx_bytes": 4096, "privileged": False,
        "disk_mode": "dir", "disk_bytes": 0,
        "bridge": "lxcbr0", "veth": "veth7a8b9c", "mac": "00:16:3e:aa:03:03",
        "root_pass": "Zf4wGj8MrPv2Qn",
        "ipv6_cfg": [], "net_off": False,
    },
    {
        "name": "kali-lab", "state": "FROZEN", "pid": 4022,
        "ipv4": ["192.168.1.44"], "ipv6": [], "mem_bytes": 734003200,
        "mem_limit": 2147483648, "cpu_seconds": 320.9, "cpu_percent": 0,
        "cpu_limit": 2, "autostart": False, "arch": "amd64",
        "os": "Kali GNU/Linux Rolling", "distro": "kali", "release": "current",
        "rootfs": "/var/lib/lxc/kali-lab/rootfs", "created_at": NOW - 86400,
        "tx_bytes": 551231, "rx_bytes": 8812345, "privileged": True,
        "disk_mode": "loop", "disk_bytes": 10737418240,
        "bridge": "lxcbr0", "veth": "vethdef012", "mac": "00:16:3e:aa:04:04",
        "root_pass": "Bq6xHt3KcNw9Rd",
        "ipv6_cfg": [], "net_off": False,
    },
]

SYSTEM = {
    "hostname": "fnos-nas", "os_name": "fnOS 1.2.3", "kernel": "6.6.38-trim",
    "arch": "amd64", "uptime_sec": 86400 * 3 + 7200, "load1": 0.62, "load5": 0.48,
    "load15": 0.41, "cpu_count": 8, "mem_total": 17179869184,
    "mem_avail": 9663676416, "swap_total": 2147483648, "swap_free": 2147483648,
    "disk_total": 2199023255552, "disk_free": 1429365116108,
    "disk_path": "/var/lib/lxc", "lxc_version": "5.0.3", "panel_version": "1.0.12",
    "port": 18080, "data_dir": "/vol1/@appdata/LXCPanel", "started_at": NOW - 3600,
    "server_time": NOW, "cgroup_version": 2, "root_running": True,
}

ENV = {
    "installed": True, "version": "5.0.3", "state_dir": "/var/lib/lxc",
    "state_writable": True, "cache_dir": "/var/cache/lxc", "cache_writable": True,
    "templates": ["alpine", "debian", "download", "ubuntu", "local", "oci"],
    "has_download": True, "cgroup_version": 2, "cgroup_writable": True,
    "pkg_manager": "apt", "root": True, "os_name": "fnOS 1.2.3",
    "kernel": "6.6.38-trim", "arch": "amd64", "containers": 4, "running": 3,
    "problems": [], "hints": [],
}

def _rel(*pairs):
    return [{"id": a, "label": b} for a, b in pairs]


PORTMAPS = [
    {"id": "pf-1a2b3c4d", "container": "ubuntu-noble", "proto": "tcp", "host_port": 8080,
     "target_port": 80, "target_ip": "10.0.3.117", "target_v6": "fd42:6c78:63::7",
     "state": "已启用", "target_ok": True, "reach": "容器端口可达",
     "created_at": NOW - 3600},
    {"id": "pf-5e6f7a8b", "container": "debian-bookworm", "proto": "tcp", "host_port": 2222,
     "target_port": 22, "target_ip": "10.0.3.24", "target_v6": "",
     "state": "已启用", "target_ok": False,
     "reach": "容器里 10.0.3.24:22 没有监听（服务没起来？）", "created_at": NOW - 7200},
    {"id": "pf-9c0d1e2f", "container": "alpine-edge", "proto": "udp", "host_port": 5353,
     "target_port": 53, "target_ip": "", "target_v6": "",
     "state": "容器未运行", "reach": "容器没有 IP，转发规则已跳过", "created_at": NOW - 900},
]
PORT_STATUS = {
    "iptables": True, "ip6tables": True, "ipv4_forward": True, "ipv6_forward": True,
    "host_ipv4": ["192.168.1.10", "10.0.3.1"],
    "host_ipv6": ["240e:3a1:2b8c:9f10::10", "fd42:6c78:63::1"],
    "bridge": "lxcbr0", "out_chain": True, "hairpin": True, "error": "",
}

# 逐层探测结果：容器直连 → 本机回环 → 各网卡地址。
# 这里故意让 2222 那条在「容器直连」就失败，用来演示分层定位。
def port_probes(rule):
    if not rule:
        return []
    hp, tp = rule["host_port"], rule["target_port"]
    out = []
    if not rule.get("target_ip"):
        out.append({"scope": "container", "label": "宿主机 → 容器", "ok": False,
                    "detail": "容器未运行或没有 IP，规则已跳过"})
        return out
    out.append({"scope": "container", "addr": "%s:%d" % (rule["target_ip"], tp),
                "label": "宿主机 → 容器 %s:%d" % (rule["target_ip"], tp),
                "ok": rule.get("target_ok", True),
                "detail": rule.get("reach") or ("端口通" if rule.get("target_ok", True) else "连接失败")})
    if rule["proto"] == "udp":
        out.append({"scope": "skip", "label": "宿主机 → 宿主端口", "ok": False,
                    "detail": "UDP 无法探测，请在容器里用 nc -u 试"})
        return out
    ok = rule.get("target_ok", True)
    for scope, ip, label in [
        ("loopback", "127.0.0.1", "宿主机 → 127.0.0.1:%d（本机访问，走 OUTPUT 链）" % hp),
        ("host", "192.168.1.10", "宿主机 → 192.168.1.10:%d（外部客户端走同一条路径）" % hp),
        ("host6", "240e:3a1:2b8c:9f10::10", "宿主机 → [240e:3a1:2b8c:9f10::10]:%d（IPv6）" % hp),
    ]:
        out.append({"scope": scope, "addr": "%s:%d" % (ip, hp), "label": label, "ok": ok,
                    "detail": "端口通" if ok else "连接失败（connect: connection refused）"})
    return out


HOSTNET = {
    "bridge": "lxcbr0", "gateway": "10.0.3.1", "subnet": "10.0.3.0/24",
    "ok": True, "has_iptables": True, "has_dnsmasq": True, "has_systemd": True,
    "proxy": "",
    "checks": [
        {"key": "bridge", "label": "桥接网卡 lxcbr0", "ok": True,
         "detail": "10.0.3.1/24（已启用）", "fixable": True},
        {"key": "forward", "label": "内核 IPv4 转发", "ok": True,
         "detail": "已开启", "fixable": True},
        {"key": "nat", "label": "NAT 出网规则", "ok": True,
         "detail": "POSTROUTING MASQUERADE 10.0.3.0/24", "fixable": True},
        {"key": "fwd", "label": "FORWARD 放行", "ok": True,
         "detail": "已放行 lxcbr0", "fixable": True},
        {"key": "dhcp", "label": "DHCP / DNS 服务", "ok": True,
         "detail": "已监听 10.0.3.1:67", "fixable": True},
        {"key": "forward6", "label": "内核 IPv6 转发", "ok": True,
         "detail": "已开启", "fixable": True},
        {"key": "mss", "label": "MSS 钳制", "ok": False, "fixable": True,
         "detail": "出口 MTU 只有 1492，但没有 TCPMSS 规则",
         "hint": "这种情况下 ping 通、http 也能下，但 https 握手会卡死（TLS 证书包被丢）"},
    ],
    "tips": [],
}

# LOGIN 模拟「密码确实写进去了，但 sshd 不让 root 用密码登录」这个最常见的坑
LOGIN = {
    "running": True, "reachable": True,
    "shadow": True, "hash_set": True, "locked": False, "hash_algo": "SHA-512",
    "shell": "/bin/bash", "shell_ok": True,
    "ssh_installed": True, "ssh_running": True, "ssh_port": 22,
    "permit_root_login": "prohibit-password", "password_auth": False,
    "init": "systemd", "pkg_mgr": "apt", "addr": "10.0.3.117",
    "need_install": False, "ready": False,
    "issues": [
        "sshd 配置为 PermitRootLogin prohibit-password，root 只能密钥登录，密码怎么改都登不上",
        "sshd 关掉了 PasswordAuthentication，只接受密钥登录",
    ],
    "tips": ["点「一键修好登录」会写入 PermitRootLogin yes / PasswordAuthentication yes 并重启 sshd"],
}


def login_payload():
    st = json.loads(json.dumps(LOGIN))
    if LOGIN.get("fixed"):
        st.update({
            "permit_root_login": "yes", "password_auth": True, "ready": True, "issues": [],
            "tips": [
                "登录条件齐全：root 有密码、shell 正常、sshd 在监听且允许 root 密码登录",
                "在宿主机或局域网执行：ssh root@10.0.3.117 -p 22",
            ],
        })
    return st


LOGIN_FIX_LOGS = [
    "root 的登录 shell 正常：/bin/bash",
    "root 密码哈希正常（SHA-512）",
    "已写入 /etc/ssh/sshd_config.d/99-lxcpanel-login.conf（PermitRootLogin yes / PasswordAuthentication yes）",
    "sshd 配置校验通过",
    "sshd 已启动并在 22 端口监听",
]

# NETDIAG 模拟「网络已经通了，但国际站点 TLS 被重置」这个真实场景
NETDIAG_BASE = [
    {"key": "addr", "label": "网卡与地址", "ok": True,
     "output": "2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500\n    inet 10.0.3.117/24 brd 10.0.3.255 scope global eth0"},
    {"key": "route", "label": "默认路由", "ok": True,
     "output": "default via 10.0.3.1 dev eth0\n10.0.3.0/24 dev eth0 scope link src 10.0.3.117"},
    {"key": "dns", "label": "DNS 配置", "ok": True, "output": "nameserver 10.0.3.1"},
    {"key": "gw", "label": "网关连通 (10.0.3.1)", "ok": True,
     "output": "2 packets transmitted, 2 received, 0% packet loss"},
    {"key": "inet", "label": "公网 IP 连通 (223.5.5.5)", "ok": True,
     "output": "2 packets transmitted, 2 received, 0% packet loss, time 1002ms"},
    {"key": "resolve", "label": "域名解析 (deb.debian.org)", "ok": True,
     "output": "151.101.2.132  deb.debian.org"}
]
NETDIAG_HTTPS_BAD = [
    {"key": "https_cn", "label": "HTTPS 国内站点 (baidu.com)", "ok": True, "output": "200"},
    {"key": "https_gh", "label": "HTTPS 国际站点 (raw.githubusercontent.com)", "ok": False,
     "output": "curl: (35) Recv failure: Connection reset by peer",
     "hint": "ping 通、HTTP 通但这里失败：国内网络访问 GitHub 的 TLS 握手会被重置，这不是容器的问题。在「运行环境 → 容器出网」填一个代理（如 http://10.0.3.1:7890）即可。"},
]
NETDIAG_HTTPS_OK = [
    {"key": "https_cn", "label": "HTTPS 国内站点 (baidu.com)", "ok": True, "output": "200"},
    {"key": "https_gh", "label": "HTTPS 国际站点 (raw.githubusercontent.com)", "ok": True, "output": "200"},
]


def hostnet_payload():
    fixed = HOSTNET.get("fixed")
    checks = [dict(c, ok=True) if fixed else c for c in HOSTNET["checks"]]
    return dict(HOSTNET, ok=True if fixed else HOSTNET["ok"], checks=checks)


def netdiag():
    proxy = HOSTNET.get("proxy") or ""
    items = list(NETDIAG_BASE)
    items += NETDIAG_HTTPS_OK if proxy else NETDIAG_HTTPS_BAD
    if proxy:
        items.append({"key": "proxy", "label": "容器代理 (%s)" % proxy, "ok": True,
                      "output": "environment: https_proxy=%s" % proxy})
    else:
        items.append({"key": "proxy", "label": "容器代理", "ok": False,
                      "output": "面板里没有配置容器代理",
                      "hint": "国内环境建议配一个：宿主机跑 mihomo 时填 http://10.0.3.1:7890（记得 allow-lan: true，让代理监听在网关上）"})
    return items


NETDIAG = netdiag()

NETFIX_LOGS = [
    "宿主机网络已正常，没有需要修复的项",
    "已添加 MSS 钳制规则（出口 MTU 1492，TCP MSS 钉到 1452），修复 https 握手卡住",
]

DIAG_SAMPLE = """LXCPanel 1.0.7 · linux/amd64 · euid=0
容器目录: /var/lib/lxc
缓存目录: /var/cache/lxc
LXC: 5.0.3

== lxc-info -n ubuntu-noble -H （耗时 38 ms）==
Name:           ubuntu-noble
State:          RUNNING
PID:            2841
IP:             10.0.3.78
CPU use:        1841.20 seconds
BlkIO use:      17890 bytes
Memory use:     268435456 bytes

== 根文件系统候选 ==
- /var/lib/lxc/ubuntu-noble/rootfs → 目录，os-release 首行: PRETTY_NAME="Ubuntu 24.04.1 LTS"

== cgroup（pid 2841）==
-- /proc/2841/cgroup --
0::/lxc.payload.ubuntu-noble
cgroup 目录: /sys/fs/cgroup/lxc.payload.ubuntu-noble
memory.current = 268435456
cpu.stat: usage_usec 1841200000 | user_usec 1203000000 | system_usec 638200000
memory.max = max
直读内存 = 268435456 字节
直读 CPU = 1841.20 秒

== 最终展示值 ==
state=RUNNING pid=2841 mem_bytes=268435456 cpu_seconds=1841.20
os="Ubuntu 24.04.1 LTS" id="ubuntu" version="24.04"
"""

DISTROS = [
    {"id": "debian", "name": "Debian", "icon": "debian", "default": "bookworm",
     "releases": _rel(("bookworm", "12 (bookworm)"), ("bullseye", "11 (bullseye)"), ("trixie", "13 (trixie)"))},
    {"id": "ubuntu", "name": "Ubuntu", "icon": "ubuntu", "default": "noble",
     "releases": _rel(("noble", "24.04 (noble)"), ("jammy", "22.04 (jammy)"), ("focal", "20.04 (focal)"))},
    {"id": "almalinux", "name": "AlmaLinux", "icon": "alma", "default": "9",
     "releases": _rel(("9", "9"), ("8", "8"))},
    {"id": "rockylinux", "name": "Rocky Linux", "icon": "rocky", "default": "9",
     "releases": _rel(("9", "9"), ("8", "8"))},
    {"id": "centos", "name": "CentOS Stream", "icon": "centos", "default": "9",
     "releases": _rel(("9", "9"), ("8", "8"))},
    {"id": "alpine", "name": "Alpine", "icon": "alpine", "default": "3.20",
     "releases": _rel(("3.20", "3.20"), ("3.19", "3.19"), ("edge", "edge（滚动）"))},
    {"id": "archlinux", "name": "Arch Linux", "icon": "arch", "default": "current",
     "releases": _rel(("current", "current（滚动）"))},
    {"id": "fedora", "name": "Fedora", "icon": "fedora", "default": "40",
     "releases": _rel(("40", "40"), ("39", "39"))},
    {"id": "opensuse", "name": "openSUSE", "icon": "suse", "default": "tumbleweed",
     "releases": _rel(("tumbleweed", "Tumbleweed（滚动）"), ("15.6", "15.6 (Leap)"))},
    {"id": "kali", "name": "Kali Linux", "icon": "kali", "default": "current",
     "releases": _rel(("current", "current（滚动）"))},
]
SERVERS = [
    {"id": "official", "name": "官方源（海外）", "host": "images.linuxcontainers.org"},
    {"id": "tuna", "name": "清华 TUNA 镜像", "host": "mirrors.tuna.tsinghua.edu.cn/lxc-images"},
    {"id": "ustc", "name": "中科大 USTC 镜像", "host": "mirrors.ustc.edu.cn/lxc-images"},
]
SETTINGS = {
    "port": 18080, "allow_lan": True, "lan_trust": True, "has_password": True,
    "image_host": "mirrors.tuna.tsinghua.edu.cn/lxc-images",
    "sample_secs": 10, "api_token": "lxc_9f3c1a77b2e84d05a6c1ef2088b7d34e",
    "token_file": "/vol1/@appdata/LXCPanel/api_token.txt",
    "data_dir": "/vol1/@appdata/LXCPanel", "version": "1.0.12", "servers": SERVERS,
    "storage": {
        "dir": "/var/lib/lxc", "default": "/var/lib/lxc", "custom": False,
        "free": 1429365116108, "total": 2199023255552, "containers": 4,
        "legacy_dir": "", "legacy_count": 0,
    },
    "cache": {
        "dir": "/var/cache/lxc", "default": "/var/cache/lxc", "custom": False,
        "free": 1429365116108, "total": 2199023255552, "bytes": 1879048192,
        "entries": 12, "legacy_dir": "", "legacy_count": 0,
    },
}

# MOCK_CUSTOM_STORAGE=1 预览"已改到数据盘、默认目录还留着容器"的状态。
if os.environ.get("MOCK_CUSTOM_STORAGE") == "1":
    SETTINGS["storage"] = {
        "dir": "/vol1/lxc", "default": "/var/lib/lxc", "custom": True,
        "free": 1429365116108, "total": 2199023255552, "containers": 4,
        "legacy_dir": "/var/lib/lxc", "legacy_count": 2,
    }
    SYSTEM["disk_path"] = "/vol1/lxc"

# MOCK_CUSTOM_CACHE=1 预览"模板缓存已改到数据盘"的状态。
if os.environ.get("MOCK_CUSTOM_CACHE") == "1":
    SETTINGS["cache"] = {
        "dir": "/vol1/lxc-cache", "default": "/var/cache/lxc", "custom": True,
        "free": 1429365116108, "total": 2199023255552, "bytes": 3221225472,
        "entries": 18, "legacy_dir": "/var/cache/lxc", "legacy_count": 4,
    }


def stats():
    import math
    pts = []
    for i in range(60):
        t = NOW - (60 - i) * 10
        pts.append({
            "t": t,
            "cpu": round(6 + 6 * math.sin(i / 7.0) + (2 if i % 11 == 0 else 0) + i * 0.05, 2),
            "mem": int(240 * 1024 * 1024 + 40 * 1024 * 1024 * math.sin(i / 9.0) + i * 900000),
        })
    return {"points": pts}


def host_points():
    import math
    pts = []
    for i in range(180):
        t = NOW - (180 - i) * 10
        cpu = 8 + 9 * math.sin(i / 13.0) + 4 * math.sin(i / 4.0) + max(0, 12 * math.sin(i / 41.0))
        mem = 7.0 * 1024 ** 3 + 1.2 * 1024 ** 3 * math.sin(i / 23.0) + i * 6 * 1024 * 1024
        pts.append({"t": t, "cpu": round(max(1, cpu), 2), "mem": int(mem)})
    return pts


# MOCK_NO_LXC=1 复现"设备未安装 LXC"的返回：空切片按 Go 的默认行为序列化成 null，
# 用来验证前端不会因为 null.length 抛异常（1.0.2 及以前点"运行环境"会停在概览页）。
if os.environ.get("MOCK_NO_LXC") == "1":
    ENV.update({
        "installed": False, "version": "", "state_writable": False,
        "templates": None, "has_download": False, "cgroup_writable": False,
        "problems": ["未检测到可用的 LXC 命令（lxc-ls / lxc-info / lxc-create）"],
        "hints": None, "containers": 0, "running": 0,
    })
    CONTAINERS.clear()


class Handler(SimpleHTTPRequestHandler):
    def __init__(self, *a, **kw):
        super().__init__(*a, directory=WWW, **kw)

    def log_message(self, *a):
        pass

    def _json(self, obj, code=200):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _sse(self, lines, status="success", error=""):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache, no-transform")
        self.end_headers()
        for ln in lines:
            self.wfile.write(("data: " + json.dumps({"line": ln}) + "\n\n").encode())
        self.wfile.write(("event: done\ndata: "
                          + json.dumps({"status": status, "error": error}) + "\n\n").encode())

    def do_GET(self):
        p = self.path.split("?")[0]
        if p.startswith("/api/"):
            # 演示用的迁移任务流：设置页点「迁移并切换」时能看到日志的样子
            if p.startswith("/api/tasks/") and p.endswith("/stream"):
                return self._sse([
                    "当前容器目录: /var/lib/lxc",
                    "目标容器目录: /vol1/lxc",
                    "当前目录下共有 4 个容器：alpine-edge、debian-bookworm、kali-lab、ubuntu-noble",
                    "待迁移数据约 3.4 GB，目标分区可用 1.3 TB",
                    "[1/4] 搬运 debian-bookworm ...",
                    "    已移动（同一分区，秒级完成）",
                    "[4/4] 搬运 ubuntu-noble ...",
                    "已更新 4 个容器的 rootfs 路径",
                    "已同步 lxc.lxcpath = /vol1/lxc 到 /etc/lxc/lxc.conf（系统开机自启同样生效）",
                    "存储位置已切换为 /vol1/lxc",
                ])
            if p == "/api/overview":
                return self._json({
                    "system": SYSTEM, "env": ENV, "containers": CONTAINERS,
                    "summary": {
                        "total": len(CONTAINERS),
                        "running": sum(1 for c in CONTAINERS if c["state"] == "RUNNING"),
                        "frozen": sum(1 for c in CONTAINERS if c["state"] == "FROZEN"),
                        "stopped": sum(1 for c in CONTAINERS if c["state"] not in ("RUNNING", "FROZEN")),
                    },
                    "usage": {"mem_used": 2130706432, "cpu_used": 16.28}, "error": "",
                    "host_points": host_points(), "sample_secs": 10,
                })
            if p == "/api/containers":
                return self._json({"containers": CONTAINERS, "error": ""})
            if p == "/api/env":
                return self._json(ENV)
            if p == "/api/distros":
                return self._json({"distros": DISTROS, "servers": SERVERS, "arch": "amd64",
                                   "has_download": True, "templates": ENV["templates"]})
            if p == "/api/settings":
                return self._json(SETTINGS)
            if p == "/api/hostnet":
                return self._json(hostnet_payload())
            if p.startswith("/api/containers/") and p.endswith("/netdiag"):
                return self._json({"probes": netdiag()})
            if p.startswith("/api/containers/") and p.endswith("/login"):
                return self._json(login_payload())
            if p == "/api/portmap":
                return self._json({"rules": PORTMAPS, "status": PORT_STATUS})
            if p.endswith("/stats"):
                return self._json(stats())
            if p.startswith("/api/containers/") and p.endswith("/diag"):
                return self._json({"text": DIAG_SAMPLE})
            if p.startswith("/api/containers/"):
                name = p.split("/")[3]
                for c in CONTAINERS:
                    if c["name"] == name:
                        return self._json(c)
                return self._json({"error": "容器不存在"}, 404)
            return self._json({"ok": True})
        return super().do_GET()

    def do_POST(self):
        p = self.path.split("?")[0]
        if p == "/api/hostnet":
            n = int(self.headers.get("Content-Length") or 0)
            body = {}
            if n:
                try:
                    body = json.loads(self.rfile.read(n) or b"{}")
                except Exception:
                    body = {}
            HOSTNET["fixed"] = True
            logs = list(NETFIX_LOGS)
            if "proxy" in body:
                HOSTNET["proxy"] = (body.get("proxy") or "").strip()
                if HOSTNET["proxy"]:
                    logs.append("容器代理已设为 " + HOSTNET["proxy"])
                    logs.append("容器 debian-bookworm：已写入代理 " + HOSTNET["proxy"])
                else:
                    logs.append("容器代理已关闭")
            return self._json({"logs": logs, "status": hostnet_payload()})
        if p.startswith("/api/containers/") and p.endswith("/login"):
            n = int(self.headers.get("Content-Length") or 0)
            body = {}
            if n:
                try:
                    body = json.loads(self.rfile.read(n) or b"{}")
                except Exception:
                    body = {}
            if body.get("install"):
                return self._json({"task_id": "t_login_fix"})
            LOGIN["fixed"] = True
            return self._json({"ok": True, "logs": LOGIN_FIX_LOGS,
                               "status": login_payload(), "need_install": False})
        if p.startswith("/api/containers/") and p.endswith("/netfix"):
            return self._json({"logs": NETFIX_LOGS})
        if p == "/api/portmap":
            n = int(self.headers.get("Content-Length") or 0)
            try:
                body = json.loads(self.rfile.read(n) or b"{}")
            except Exception:
                return self._json({"ok": False, "error": "请求格式错误"}, 400)
            for r in PORTMAPS:
                if r["proto"] == body.get("proto") and r["host_port"] == body.get("host_port"):
                    return self._json({"ok": False, "error": "宿主端口已被占用"}, 409)
            PORTMAPS.append({
                "id": "pf-new%d" % len(PORTMAPS), "container": body.get("container"),
                "proto": body.get("proto", "tcp"), "host_port": body.get("host_port"),
                "target_port": body.get("target_port"), "target_ip": "192.168.1.24",
                "state": "已启用", "created_at": NOW,
            })
            return self._json({"ok": True, "logs": [
                "IPv4 转发规则已同步（%d 条）" % len(PORTMAPS),
            ]})
        if "/api/portmap/" in p and p.endswith("/probe"):
            rid = p.split("/api/portmap/")[1].rsplit("/", 1)[0]
            rule = next((r for r in PORTMAPS if r["id"] == rid), None)
            return self._json({"probes": port_probes(rule)})
        if p == "/api/portmap/sync":
            return self._json({"ok": True, "logs": [
                "IPv4 转发规则已同步（%d 条）" % len(PORTMAPS),
                "IPv6 转发规则已同步（0 条）",
            ]})
        if p.startswith("/api/containers/") and p.endswith("/ipv6"):
            return self._json({"ok": True, "address": "fd42:6c78:63::5", "need_restart": True, "logs": [
                "已开启宿主机 IPv6 转发（net.ipv6.conf.all.forwarding=1）",
                "已在 lxcbr0 上配置内网 IPv6 网关 fd42:6c78:63::1/64",
                "已为容器分配 IPv6 地址 fd42:6c78:63::5/64，网关 fd42:6c78:63::1",
                "容器正在运行，重启后 IPv6 生效",
            ]})
        return self._json({"task_id": "t_demo123"})

    def do_PUT(self):
        p = self.path.split("?")[0]
        if p.startswith("/api/containers/") and p.endswith("/password"):
            n = int(self.headers.get("Content-Length") or 0)
            try:
                body = json.loads(self.rfile.read(n) or b"{}")
            except Exception:
                body = {}
            name = p.split("/")[3]
            pw = body.get("password") or "Rn5wTz9KqLb2Xc"
            for c in CONTAINERS:
                if c["name"] == name:
                    c["root_pass"] = pw
            return self._json({
                "password": pw,
                "write": {
                    "method": "chpasswd", "changed": True, "verified": True, "locked": False,
                    "algo": "SHA-512", "detail": "已写入 /etc/shadow 并通过回读校验（SHA-512）",
                },
                "login": login_payload(),
            })
        return self._json({"ok": True})

    def do_DELETE(self):
        p = self.path.split("?")[0]
        if p.startswith("/api/portmap/"):
            rid = p.rsplit("/", 1)[-1]
            for i, r in enumerate(PORTMAPS):
                if r["id"] == rid:
                    PORTMAPS.pop(i)
                    break
            return self._json({"ok": True, "logs": ["IPv4 转发规则已同步（%d 条）" % len(PORTMAPS)]})
        return self._json({"task_id": "t_demo123"})


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8899
    srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print("mock server on http://127.0.0.1:%d" % port)
    srv.serve_forever()
