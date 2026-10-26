# LXCPanel API 参考

面板默认监听 `0.0.0.0:18080`，全部接口返回 JSON（任务日志与终端为 SSE 流）。

## 认证

| 方式 | 说明 |
| --- | --- |
| `X-API-Token: <token>` | 推荐，适用于脚本 |
| `Authorization: Bearer <token>` | 同上 |
| `?token=<token>` | 便于快速调试 / `EventSource` 场景；浏览器打开时会自动写入 Cookie |
| Cookie `lxcpanel_sess` | 管理密码登录后签发，有效期 30 天（面板 UI 使用） |
| Cookie `lxcpanel_token` | 用 `?token=` 引导写入，等价于 Token 认证 |
| 本机 / 飞牛网关 | 回环地址、本机网卡地址、或 Unix Socket + 代理头，视为已认证 |

来源判定规则（按顺序命中即放行）：

1. 带有效会话 Cookie（`lxcpanel_sess`）→ 放行；
2. 带有效 API Token → 放行；
3. 本机（回环 / 本机网卡 IP / 带代理头的 Unix Socket 反代）→ 放行；
4. 内网地址（RFC1918、CGNAT、IPv6 ULA）→ 「内网免密登录」开启时放行（默认开启）；
5. 其它地址 → 返回密码登录页（HTML）/ `401`（JSON）；
6. 「允许局域网访问」关闭时，内网无凭据请求返回 `403`。

Token 存放于 `${TRIM_PKGVAR}/config.json`（0600），并同步到 `${TRIM_PKGVAR}/api_token.txt`（启动时写入 `info.log`）。面板「设置与 API」页可查看 / 复制 / 重置。

```bash
# 无法进入面板时从服务器取回 Token
sudo cat /vol1/@appdata/LXCPanel/api_token.txt
```

```bash
export TOKEN=lxc_xxxxxxxx
export HOST=http://192.168.1.10:18080
```

## 系统

### `GET /api/overview`

一次性返回面板首屏所需的全部数据。

```json
{
  "system": { "hostname": "fnos-nas", "os_name": "fnOS 1.2.3", "kernel": "6.6.38-trim",
              "arch": "amd64", "uptime_sec": 315600, "load1": 0.62, "cpu_count": 8,
              "mem_total": 17179869184, "mem_avail": 9663676416,
              "disk_total": 2199023255552, "disk_free": 1429365116108,
              "lxc_version": "5.0.3", "panel_version": "1.0.0",
              "port": 18080, "cgroup_version": 2, "root_running": true },
  "env": { "installed": true, "has_download": true, "problems": [], "hints": [] },
  "containers": [ /* 见下方容器对象 */ ],
  "summary": { "total": 4, "running": 2, "frozen": 1, "stopped": 1 },
  "usage": { "mem_used": 2130706432, "cpu_used": 16.28 },
  "host_points": [ { "t": 1759000000, "cpu": 12.4, "mem": 7516192768 } ],
  "sample_secs": 10
}
```

### `GET /api/system` · `GET /api/env`

分别返回主机信息与环境自检结果（`installed`、`version`、`templates`、`has_download`、`cgroup_version`、`cgroup_writable`、`state_writable`、`pkg_manager`、`problems[]`、`hints[]`）。

### `POST /api/env/install`

安装 `lxc` 与 `lxc-templates`，返回异步任务：

```json
{ "task_id": "t_9f2c1a7b0d3e" }
```

### `GET /api/distros`

创建向导所需的发行版目录、镜像源列表与当前架构。

```json
{
  "distros": [
    { "id": "debian", "name": "Debian", "icon": "debian", "default": "bookworm",
      "releases": [
        { "id": "bookworm", "label": "12 (bookworm)" },
        { "id": "bullseye", "label": "11 (bullseye)" },
        { "id": "trixie",   "label": "13 (trixie)" }
      ] }
  ],
  "servers": [ { "id": "tuna", "name": "清华 TUNA 镜像", "host": "mirrors.tuna.tsinghua.edu.cn/lxc-images" } ],
  "arch": "amd64", "has_download": true, "templates": ["download"]
}
```

`releases[].id` 是传给 `lxc-create` 的实际值（Debian / Ubuntu 用代号），`label` 是界面展示写法（版本号加代号）。创建容器时 `release` 字段填 `id`。

## 容器

### 容器对象

```json
{
  "name": "debian-bookworm", "state": "RUNNING", "pid": 2841,
  "ipv4": ["192.168.1.24"], "ipv6": [],
  "mem_bytes": 268435456, "mem_limit": 2147483648,
  "cpu_seconds": 1841.2, "cpu_percent": 3.42, "cpu_limit": 2,
  "autostart": true, "arch": "amd64", "os": "Debian GNU/Linux 12 (bookworm)",
  "distro": "debian", "release": "12",
  "rootfs": "/var/lib/lxc/debian-bookworm/rootfs",
  "created_at": 1758400000, "tx_bytes": 18432012, "rx_bytes": 92830211,
  "privileged": false,
  "disk_mode": "dir", "disk_bytes": 0,
  "bridge": "lxcbr0", "veth": "veth1a2b3c", "mac": "00:16:3e:aa:bb:cc",
  "ipv6_cfg": ["fd42:6c78:63::2/64"], "net_off": false,
  "root_pass": "Km7tRq2WpzXa4V"
}
```

`disk_mode`：`dir`（rootfs 为普通目录，容量不限制）或 `loop`（独立 ext4 镜像）。`disk_bytes` 为镜像容量，`dir` 模式下为 0。

运行中的容器，`mem_bytes` / `cpu_seconds` 优先读 `/proc/<pid>/cgroup` 指向的 cgroup（`memory.current`、`cpu.stat`），读不到才退回 `lxc-info` 的数值；`os` 取自容器内 `/etc/os-release`，读不到时依次回退到创建时记录的信息（如 `Debian 12 (bookworm)`）。

`state` 取值：`RUNNING` / `STOPPED` / `FROZEN` / `STARTING` / `STOPPING` / `UNKNOWN`。

### `GET /api/containers`

```json
{ "containers": [ /* ... */ ], "error": "" }
```

### `POST /api/containers`

创建容器（异步）。请求体：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | 必填，`[A-Za-z0-9][A-Za-z0-9_.-]{0,62}` |
| `distro` | string | 发行版 ID，如 `debian` |
| `release` | string | 版本，如 `bookworm` |
| `arch` | string | 默认取服务端架构 `amd64` / `arm64` |
| `memory_mb` | int | 内存上限（MiB），0 表示不限 |
| `cpus` | float | CPU 上限（核），0 表示不限 |
| `autostart` | bool | 写入 `lxc.start.auto` |
| `server` | string | 镜像源主机名，如 `mirrors.tuna.tsinghua.edu.cn/lxc-images` |
| `start_after` | bool | 创建完成后立即启动 |
| `disk_gb` | int | 磁盘容量（GB），`0` 表示不限制（dir 存储）；大于 0 时用 `-B loop` 创建 ext4 镜像，上限 1024 |
| `network` | string | `nat`（默认，veth 桥接 lxcbr0）或 `none`（不配网卡） |
| `ipv6` | string | `on` 时创建后自动分配内网 IPv6，`off`（默认）不启用 |

```bash
curl -s -X POST -H "X-API-Token: $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"ubuntu-dev","distro":"ubuntu","release":"noble",
       "memory_mb":2048,"cpus":2,"autostart":true,"start_after":true,
       "disk_gb":10,
       "server":"mirrors.tuna.tsinghua.edu.cn/lxc-images"}' \
  $HOST/api/containers
```

返回：

```json
{ "task_id": "t_5f3c1a", "root_pass": "Km7tRq2WpzXa4V" }
```

`root_pass` 是创建时自动生成的 14 位随机 root 密码（剔除 `0O1lI` 等易混字符），容器创建完成后由面板通过 `lxc-attach chpasswd` 写进容器，并记在 `meta/containers.json` 里，因此容器详情与列表都能看到。

### `GET /api/containers/{name}`

容器详情（含 `cpu_percent` 最新值）。

### `POST /api/containers/{name}/action`

```json
{ "action": "start" }
```

`action`：`start` / `stop` / `restart` / `kill` / `freeze` / `unfreeze`。

```bash
curl -s -X POST -H "X-API-Token: $TOKEN" -H "Content-Type: application/json" \
  -d '{"action":"restart"}' $HOST/api/containers/ubuntu-dev/action
```

### `DELETE /api/containers/{name}`

停止并删除容器（异步，返回 `task_id`）。容器数据将**永久删除**。

### `PUT /api/containers/{name}/limits`

```json
{ "memory_mb": 4096, "cpus": 2, "autostart": true }
```

写入 `lxc.cgroup2.memory.max` / `lxc.cgroup2.cpu.max`；容器运行中时同步通过 `lxc-cgroup` 热更新。`memory_mb` 或 `cpus` 传 0 表示移除限制。

### `POST /api/containers/{name}/disk`

调整磁盘容量（异步任务，返回 `task_id`）。要求容器处于 `STOPPED`。

```json
{ "size_gb": 20 }
```

| 当前 | `size_gb` | 行为 |
| --- | --- | --- |
| `dir` | 0 | 不变（本来就不限制） |
| `dir` | > 0 | 创建 ext4 镜像、复制 rootfs 进去，转为 `loop:` 存储（容量受限） |
| `loop` | 大于当前 | `truncate` + `e2fsck -f` + `resize2fs` 扩容（**不能缩小**） |
| `loop` | 0 | 把镜像内容复制回目录、删除镜像，恢复为不限制 |

```bash
curl -s -X POST -H "X-API-Token: $TOKEN" -H "Content-Type: application/json" \
  -d '{"size_gb":20}' $HOST/api/containers/ubuntu-dev/disk
```

### `POST /api/containers/{name}/ipv6`

开启或关闭容器的内网 IPv6（`fd42:6c78:63::/64`）。开启时会顺带打开宿主机 IPv6 转发、给桥接加 `::1` 网关。改动写进容器 config，**需要重启容器生效**。

```json
{ "enable": true }
```

```json
{ "ok": true, "address": "fd42:6c78:63::5", "need_restart": true,
  "logs": ["已开启宿主机 IPv6 转发（net.ipv6.conf.all.forwarding=1）", "..."] }
```

### `PUT /api/containers/{name}/password`

修改容器 root 密码，传 `password` 指定新密码（6–128 位，不能含换行），或传 `random: true` 让面板重新随机生成。容器没运行时面板会临时启动、写完后停回原状态，因此这个接口可能需要几秒。

写入后会**回读 `/etc/shadow` 比对哈希**：命令成功但哈希没变会返回 `500`（"密码没有写进容器"），不再假成功。

```json
{ "password": "MyNewPass123" }
```
```json
{ "random": true }
```

响应里的 `write` 描述这次写入是否真的生效，`login` 是同一时刻的登录条件快照（见下一个接口）：

```json
{
  "password": "Rn5wTz9KqLb2Xc",
  "write": {
    "method": "chpasswd", "changed": true, "verified": true, "locked": false,
    "algo": "SHA-512", "detail": "已写入 /etc/shadow 并通过回读校验（SHA-512）"
  },
  "login": { "ready": false, "ssh_installed": true, "permit_root_login": "prohibit-password", "issues": ["…"] }
}
```

### `GET /api/containers/{name}/login`

检查容器**能不能用 root 密码登录**（密码写进去 ≠ 登得上）。容器未运行时返回 `running: false`，不会自动启动。

```json
{
  "running": true, "reachable": true,
  "shadow": true, "hash_set": true, "locked": false, "hash_algo": "SHA-512",
  "shell": "/bin/bash", "shell_ok": true,
  "ssh_installed": true, "ssh_running": true, "ssh_port": 22,
  "permit_root_login": "prohibit-password", "password_auth": false,
  "init": "systemd", "pkg_mgr": "apt", "addr": "10.0.3.117",
  "need_install": false, "ready": false,
  "issues": [
    "sshd 配置为 PermitRootLogin prohibit-password，root 只能密钥登录，密码怎么改都登不上",
    "sshd 关掉了 PasswordAuthentication，只接受密钥登录"
  ],
  "tips": ["点「一键修好登录」会写入 PermitRootLogin yes / PasswordAuthentication yes 并重启 sshd"]
}
```

判定 `ready` 的条件：`hash_set && !locked && shell_ok && ssh_installed && ssh_running` 且端口在监听、`password_auth` 为真、`permit_root_login == "yes"`。

### `POST /api/containers/{name}/login`

修复登录条件。`install: false`（默认）只做秒级操作：改回正常 shell、解锁 / 重写 root 密码、写 sshd 配置（优先 `/etc/ssh/sshd_config.d/99-lxcpanel-login.conf`，老版本改主配置并用 `sshd -t` 校验，失败回滚）、`ssh-keygen -A`、启动 sshd，然后同步返回：

```json
{ "ok": true, "logs": ["root 的登录 shell 正常：/bin/bash", "sshd 已启动并在 22 端口监听"],
  "status": { "ready": true, "…": "…" }, "need_install": false }
```

`install: true` 会额外安装 `openssh-server`（apt / apk / dnf / yum / zypper），耗时长，走任务返回 `{"task_id": "…"}`，用 `GET /api/tasks/{id}/stream` 看日志。容器没装 sshd 且 `install` 为假时，返回 `need_install: true` 而不是报错。

### `GET /api/containers/{name}/netdiag`

在容器里跑一组网络检查（需要容器处于运行状态），用来定位「ping 不通」断在哪一层。

```json
{
  "probes": [
    { "key": "addr", "label": "网卡与地址", "ok": true, "output": "2: eth0: ... inet 10.0.3.117/24 ..." },
    { "key": "route", "label": "默认路由", "ok": true, "output": "default via 10.0.3.1 dev eth0" },
    { "key": "dns", "label": "DNS 配置", "ok": true, "output": "nameserver 10.0.3.1" },
    { "key": "gw", "label": "网关连通 (10.0.3.1)", "ok": true, "output": "2 packets transmitted, 2 received" },
    { "key": "inet", "label": "公网 IP 连通 (223.5.5.5)", "ok": false,
      "output": "2 packets transmitted, 0 received",
      "hint": "网关通但公网不通：宿主机缺 ip_forward / MASQUERADE（点「一键修复」）" },
    { "key": "resolve", "label": "域名解析 (deb.debian.org)", "ok": true, "output": "151.101.2.132  deb.debian.org" },
    { "key": "https_cn", "label": "HTTPS 国内站点 (baidu.com)", "ok": true, "output": "200" },
    { "key": "https_gh", "label": "HTTPS 国际站点 (raw.githubusercontent.com)", "ok": false,
      "output": "curl: (35) Recv failure: Connection reset by peer",
      "hint": "ping 通、HTTP 通但这里失败：国内网络访问 GitHub 的 TLS 握手会被重置……填一个代理即可。" },
    { "key": "proxy", "label": "容器代理 (http://10.0.3.1:7890)", "ok": true,
      "output": "environment: https_proxy=http://10.0.3.1:7890" }
  ]
}
```

`key` 依次为 `addr` / `route` / `dns` / `gw` / `inet` / `resolve` / `https_cn` / `https_gh` / `proxy`，`ok=false` 时带 `hint` 说明该层该怎么修。

- `https_cn` / `https_gh` 用容器里的 `curl`（没有则退到 `wget`）各发一次 `HEAD` 请求，判定标准是拿到 `200` / `3xx` / `403`。前者测「HTTPS 是否整体可用」，后者测「国际站点是否被重置」，两者一对比就能分清是链路问题还是站点问题。
- `proxy` 在配置了容器代理时检查 `/etc/environment` 与 apt 配置里有没有落地的代理值；未配置时固定返回 `ok=false` 并提示怎么配。
- 容器里既没有 `curl` 也没有 `wget` 时，`https_*` 两项的输出会直接提示装一个。

### `POST /api/containers/{name}/netfix`

修复宿主机侧让容器出网所需的桥接、内核转发、NAT 与 DHCP/DNS（等价于对 `POST /api/hostnet` 的调用），返回执行日志。

```json
{ "logs": ["已添加 NAT 出网规则 10.0.3.0/24 → MASQUERADE",
           "已添加 MSS 钳制规则（出口 MTU 1492，TCP MSS 钉到 1452），修复 https 握手卡住",
           "容器 debian-bookworm：已写入代理 http://10.0.3.1:7890"],
  "status": { /* 同 GET /api/hostnet */ } }
```

配置了容器代理时，这个接口会顺带把代理重新写一遍到该容器（等价于调用一次 `POST /api/hostnet` 并带 `proxy`）。

### 宿主机网络

#### `GET /api/hostnet`

容器出网依赖的宿主机侧体检结果。

```json
{
  "bridge": "lxcbr0", "gateway": "10.0.3.1", "subnet": "10.0.3.0/24",
  "ok": false, "has_iptables": true, "has_dnsmasq": true, "has_systemd": true,
  "proxy": "http://10.0.3.1:7890",
  "checks": [
    { "key": "bridge", "label": "桥接网卡 lxcbr0", "ok": true, "detail": "10.0.3.1/24（已启用）", "fixable": true },
    { "key": "forward", "label": "内核 IPv4 转发", "ok": true, "detail": "已开启", "fixable": true },
    { "key": "nat", "label": "NAT 出网规则", "ok": false, "detail": "缺少 10.0.3.0/24 的 MASQUERADE",
      "hint": "没有它容器只能通到宿主机，出不去公网", "fixable": true },
    { "key": "fwd", "label": "FORWARD 放行", "ok": true, "detail": "已放行 lxcbr0", "fixable": true },
    { "key": "dhcp", "label": "DHCP / DNS 服务", "ok": false, "detail": "桥接上没有 DHCP 服务",
      "hint": "没有 DHCP，容器拿不到 IP（网卡在、地址空）", "fixable": true },
    { "key": "forward6", "label": "内核 IPv6 转发", "ok": true, "detail": "已开启", "fixable": true },
    { "key": "mss", "label": "MSS 钳制", "ok": false, "fixable": true,
      "detail": "出口 MTU 只有 1492，但没有 TCPMSS 规则",
      "hint": "这种情况下 ping 通、http 也能下，但 https 握手会卡死（TLS 证书包被丢）" }
  ],
  "tips": ["点「一键修复」会自动补齐桥接、转发、NAT 和 DHCP；已存在的容器可能需要重启才拿到新地址。"]
}
```

`ok` 只看 `bridge` / `forward` / `nat` / `fwd` / `dhcp` 五项；`forward6` 只影响 IPv6，`mss` 是疑难项（只在宿主上行 MTU 小于 1500 时出现），两者都不参与 `ok` 判定。

`proxy` 是当前配的容器出网代理，为空表示没配。

#### `POST /api/hostnet`

一键修复并返回日志与修复后的状态：

```json
{ "logs": ["已开启 net.ipv4.ip_forward=1", "已添加 NAT 出网规则 10.0.3.0/24 → MASQUERADE",
           "已启动面板自带 dnsmasq（地址池 10.0.3.100 - 10.0.3.240，DNS 10.0.3.1）"],
  "status": { /* 同 GET /api/hostnet */ } }
```

修复顺序：`ip_forward` → 网桥（`ip link add` / 补地址 / `ip link set up`）→ `POSTROUTING MASQUERADE` → `FORWARD` 放行 → **MSS 钳制** → DHCP/DNS（先试 `systemctl start lxc-net`，失败则用面板自带的 `dnsmasq` 配置）。宿主机没装 `dnsmasq` 时退化为给容器写静态 IP（`10.0.3.150` 起）并补 `/etc/resolv.conf`。

##### 设置容器出网代理

请求体可选带 `proxy`，传了就顺手保存并下发到所有运行中的容器：

```json
{ "proxy": "http://10.0.3.1:7890" }
```

```json
{ "proxy": "" }
```

- 值可以是 `http://` / `https://` / `socks5://`，省略协议时按 `http://` 处理，省略端口时 http 默认 `7890`、socks 默认 `1080`；
- 空字符串表示关闭代理，四个落点会同步删干净；
- 不带 `proxy` 字段则只做修复，不动代理配置；
- 返回的 `logs` 里会包含每个容器写入的结果（停着的容器会跳过，等它启动时自动补写）。

代理落点：`/etc/environment`（sshd/PAM、systemd 服务）、`/etc/apt/apt.conf.d/99lxcpanel-proxy`（apt 不读环境变量）、`/etc/profile.d/lxcpanel-proxy.sh`（登录 shell）、`/etc/bash.bashrc` 与 `/root/.bashrc`（交互式 shell）。`no_proxy` 默认排除 `10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`，宿主机与内网仍然直连。

同一份配置也会在 `GET/PUT /api/settings` 的 `proxy_url` 字段里出现。

### 端口转发

#### `GET /api/portmap`

```json
{
  "rules": [
    { "id": "pf-1a2b3c4d", "container": "ubuntu-dev", "proto": "tcp",
      "host_port": 8080, "target_port": 80,
      "target_ip": "10.0.3.5", "target_v6": "fd42:6c78:63::5",
      "state": "已启用", "target_ok": true, "reach": "容器端口可达",
      "created_at": 1758400000 }
  ],
  "status": {
    "iptables": true, "ip6tables": true,
    "ipv4_forward": true, "ipv6_forward": true,
    "host_ipv4": ["192.168.1.10", "10.0.3.1"],
    "host_ipv6": ["240e:3a1:2b8c:9f10::10", "fd42:6c78:63::1"],
    "bridge": "lxcbr0", "out_chain": true, "hairpin": true, "error": ""
  }
}
```

- `state` 为运行时状态：`已启用` / `容器未运行` / `缺少 iptables`；
- `target_ip` / `target_v6` 是规则实际会转发到的容器地址（v6 跳过 `fe80::` 链路本地地址）；
- `target_ok` 是从宿主机直连容器目标端口的结果（UDP 固定为 `false`，`reach` 里会说明 UDP 不探测）；
- `status.out_chain` 表示 `nat/OUTPUT` 里的 `LXCPANEL-OUT` 是否就位（宿主机本机访问靠它），
  `status.hairpin` 表示 NAT 回环（hairpin SNAT）是否就位。

#### `POST /api/portmap`

```json
{ "container": "ubuntu-dev", "proto": "tcp", "host_port": 8080, "target_port": 80 }
```

返回 `{"ok": true, "logs": [...]}`。宿主端口与本机已有服务冲突、或与本面板已有规则冲突时返回 `409`。

#### `POST /api/portmap/sync`

按各容器当前 IP 重建全部规则，返回执行日志：

```json
{ "ok": true, "logs": [
  "IPv4 转发已同步（3 条规则，PREROUTING + OUTPUT + FORWARD）",
  "IPv6 转发已同步（2 条规则）",
  "已补齐 NAT 回环（宿主机本机 / 容器互访访问宿主端口）"
] }
```

每次都会：清空 `nat` / `filter` 的 `LXCPANEL` 与 `nat` 的 `LXCPANEL-OUT` → 按当前地址重填 →
把三条链核对并顶到 `PREROUTING` / `OUTPUT` / `FORWARD` 的第一行 → 打开 IPv4 / IPv6 转发 →
补 hairpin SNAT 与 `ip6tables POSTROUTING MASQUERADE`。

#### `POST /api/portmap/{id}/probe`

从宿主机逐层探测一条规则的连通性，用来回答「为什么连不上」。

```json
{
  "probes": [
    { "scope": "container", "addr": "10.0.3.5:80", "label": "宿主机 → 容器 10.0.3.5:80",
      "ok": true, "detail": "容器端口可达" },
    { "scope": "loopback", "addr": "127.0.0.1:8080", "label": "宿主机 → 127.0.0.1:8080（本机访问，走 OUTPUT 链）",
      "ok": true, "detail": "端口通" },
    { "scope": "host", "addr": "192.168.1.10:8080", "label": "宿主机 → 192.168.1.10:8080（外部客户端走同一条路径）",
      "ok": false, "detail": "连接失败（dial tcp 192.168.1.10:8080: connect: connection refused）" },
    { "scope": "host6", "addr": "[240e:3a1:2b8c:9f10::10]:8080", "label": "宿主机 → [240e:3a1:2b8c:9f10::10]:8080（IPv6）",
      "ok": true, "detail": "端口通" }
  ]
}
```

`scope` 取值：`container`（直连容器端口）/ `loopback`（本机回环，验证 OUTPUT 链与回环）/
`host`（宿主 IPv4 网卡地址）/ `host6`（宿主 IPv6 网卡地址）/ `skip`（UDP 无法探测）。
按顺序看第一个 `ok: false` 的项就知道断在哪一层。

#### `DELETE /api/portmap/{id}`

删除规则并立即重建。

### `GET /api/containers/{name}/diag`

导出诊断报告（纯文本，放在 `text` 字段）。用于排查「系统列空白 / 内存 0 B / CPU 恒为 0%」这类读取异常，内容包含 `lxc-info` 原始输出与耗时、config 原文、rootfs 候选目录的可读性、`/proc/<pid>/cgroup` 与 `memory.current` / `cpu.stat`。

```bash
curl -s -H "X-API-Token: $TOKEN" $HOST/api/containers/ubuntu-dev/diag
```

### `GET /api/containers/{name}/logs?lines=200`

返回 `/var/log/lxc/{name}.log` 的最后 N 行（默认 300，上限 4000）。

### `GET /api/containers/{name}/config`

返回容器原始配置文件文本（只读）。

### `GET /api/containers/{name}/stats`

CPU / 内存历史采样点（最多 360 个，间隔取面板配置）：

```json
{ "points": [ { "t": 1759000000, "cpu": 3.42, "mem": 268435456 } ] }
```

## 终端

| 接口 | 说明 |
| --- | --- |
| `POST /api/containers/{name}/terminal` | 创建会话，返回 `{ "id": "s_xxx", "shell": "/bin/bash" }`，要求容器处于 `RUNNING` |
| `GET /api/term/{id}/stream` | SSE 输出流，`data: {"d":"<base64>"}`，结束事件 `event: end` |
| `POST /api/term/{id}/input` | 发送按键，`{"d":"<base64>"}` |
| `POST /api/term/{id}/resize` | `{"cols":120,"rows":32}` |
| `DELETE /api/term/{id}` | 关闭会话 |

会话 30 分钟无操作自动回收。

## 异步任务

创建容器、删除容器、安装运行环境均为异步任务。

### `GET /api/tasks/{id}`

```json
{ "id": "t_9f2c", "kind": "create", "title": "创建容器 ubuntu-dev",
  "status": "running", "error": "", "output": ["..."],
  "started_at": 1759000000, "finished_at": 0 }
```

`status`：`running` / `success` / `failed`。

### `GET /api/tasks/{id}/stream`

SSE 流，逐行推送：

```
data: {"line":"Downloading the rootfs"}
...
event: done
data: {"status":"success","error":""}
```

## 设置

### `GET /api/settings` · `PUT /api/settings`

```json
{ "allow_lan": true,
  "lan_trust": true,
  "image_host": "mirrors.tuna.tsinghua.edu.cn/lxc-images",
  "sample_secs": 10 }
```

- `lan_trust`：内网免密登录。开启时内网设备可直接打开面板；关闭后内网也需输入管理密码。
- `allow_lan`：是否接受内网来源。关闭后内网无凭据请求返回 `403`。
- `sample_secs` 取值 3–120。`GET` 额外返回 `api_token`、`token_file`、`has_password`、`port`、`data_dir`、`version`、`servers`，以及 `storage` / `cache` 两个存储状态对象：

```json
{ "storage": { "dir": "/vol1/lxc", "default": "/var/lib/lxc", "custom": true,
               "free": 1429365116108, "total": 2199023255552,
               "containers": 4, "legacy_dir": "/var/lib/lxc", "legacy_count": 2 },
  "cache":   { "dir": "/var/cache/lxc", "default": "/var/cache/lxc", "custom": false,
               "free": 1429365116108, "total": 2199023255552,
               "bytes": 1879048192, "entries": 12,
               "legacy_dir": "", "legacy_count": 0 } }
```

### `POST /api/storage/switch`

切换容器目录或模板缓存目录（异步任务，返回 `task_id`）。

```json
{ "path": "/vol1/lxc", "migrate": true, "kind": "containers" }
```

| 字段 | 说明 |
| --- | --- |
| `path` | 目标目录，绝对路径且至少两级；容器目录不能落在 `/proc` `/sys` `/etc` `/tmp` `/var/log` 等系统目录 |
| `migrate` | `true` 迁移已有数据（容器会改写 `lxc.rootfs.path`、`loop:` 路径以及 `/etc/lxc/lxc.conf` 的 `lxc.lxcpath`）；`false` 只切换指针 |
| `kind` | `containers`（默认）或 `cache`（镜像缓存，切换后创建容器带 `LXC_CACHE_PATH`） |

容器迁移要求全部容器为 `STOPPED`；迁移期间创建 / 启停 / 删除容器返回 `409`。

### `POST /api/token/rotate`

重置 API Token，旧 Token 立即失效；同时为当前浏览器写入新 Cookie，并更新 `api_token.txt`。

### 登录相关

| 接口 | 说明 |
| --- | --- |
| `GET /api/auth/state` | 返回 `has_password` / `needs_setup` / `authed` / `setup_grace` |
| `POST /api/auth/login` | `{"password":"..."}` → 成功签发 30 天会话 Cookie；连续 6 次失败锁定来源 IP 5 分钟（`429`） |
| `POST /api/auth/setup` | 首次设置管理密码（仅未设置时可用，且需在启动后 2 小时内或来自本机） |
| `POST /api/auth/password` | `{"old":"...","new":"..."}` 修改密码（需已登录） |
| `POST /api/auth/logout` | 清除会话 Cookie |

### `POST /api/login`
`{"token":"lxc_xxx"}`，校验通过后写入 Cookie 并返回 `{"ok":true}`；用于非回环地址访问时的手动登录。
