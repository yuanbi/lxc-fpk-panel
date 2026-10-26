package main

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// 「密码改了但登不上」和「密码没写进去」是两件事。前者是登录链路的问题：
//
//  1. 官方 download 模板默认不带 openssh-server，SSH 直接 Connection refused；
//  2. Debian / Ubuntu 的 sshd 默认 PermitRootLogin prohibit-password，root 密码登录被拒；
//  3. 有的镜像把 root 的 shell 改成 nologin，密码对了也立刻被踢出；
//  4. /etc/shadow 里 root 是 ! 开头（锁定），或者根本没有哈希；
//  5. sshd 装了但没启动 / 没有 host key，端口上没人监听。
//
// 这里把整条链路探一遍，并把能自动修的一次性修好。

// LoginStatus 是容器能否用 root 密码登录的完整判定。
type LoginStatus struct {
	Running   bool `json:"running"`
	Reachable bool `json:"reachable"` // 能否进容器执行命令

	Shadow   bool   `json:"shadow"`    // 有 /etc/shadow
	HashSet  bool   `json:"hash_set"`  // root 有真实密码哈希
	Locked   bool   `json:"locked"`    // root 被锁定
	HashAlgo string `json:"hash_algo"` // SHA-512 / yescrypt …

	Shell   string `json:"shell"`
	ShellOK bool   `json:"shell_ok"`

	SSHInstalled    bool   `json:"ssh_installed"`
	SSHRunning      bool   `json:"ssh_running"`
	SSHPort         int    `json:"ssh_port"`
	PermitRootLogin string `json:"permit_root_login"`
	PasswordAuth    bool   `json:"password_auth"`

	Init         string `json:"init"`
	PkgMgr       string `json:"pkg_mgr"`
	Addr         string `json:"addr"`
	NeedInstall  bool   `json:"need_install"`
	Ready        bool   `json:"ready"`
	Issues       []string `json:"issues"`
	Tips         []string `json:"tips"`
}

// loginProbeScript 一次采集所有判定条件。输出 key=value 行，最后用 done=1 收尾，
// 这样即使 lxc-attach 中途失败也能识别出是「没进得去」而不是「全部为空」。
const loginProbeScript = `export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
echo "shadow=$([ -f /etc/shadow ] && echo 1 || echo 0)"
echo "hash=$(grep '^root:' /etc/shadow 2>/dev/null | cut -d: -f2)"
[ -z "$(grep '^root:' /etc/shadow 2>/dev/null | cut -d: -f2)" ] && echo "phash=$(grep '^root:' /etc/passwd 2>/dev/null | cut -d: -f2)"
echo "shell=$(grep '^root:' /etc/passwd 2>/dev/null | cut -d: -f7)"
echo "sshb=$(command -v sshd 2>/dev/null || echo "")"
echo "sshrun=$( (pgrep -x sshd >/dev/null 2>&1 && echo 1) || echo 0)"
echo "port=$(sshd -T 2>/dev/null | awk '/^port /{print $2; exit}')"
echo "listen=$( (ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | grep -c ":22 ")"
echo "prl=$(sshd -T 2>/dev/null | awk '/^permitrootlogin /{print $2; exit}')"
echo "pwa=$(sshd -T 2>/dev/null | awk '/^passwordauthentication /{print $2; exit}')"
echo "init=$(cat /proc/1/comm 2>/dev/null)"
echo "pkg=$(command -v apt-get >/dev/null 2>&1 && echo apt || (command -v apk >/dev/null 2>&1 && echo apk || (command -v dnf >/dev/null 2>&1 && echo dnf || (command -v zypper >/dev/null 2>&1 && echo zypper || (command -v yum >/dev/null 2>&1 && echo yum || echo none)))))"
echo "addr=$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4; exit}' | cut -d/ -f1)"
echo "done=1"`

// parseKV 把 key=value 行解析成 map，value 里允许出现 = 和空串。
func parseKV(out string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		kv[line[:i]] = line[i+1:]
	}
	return kv
}

// shellUsable 判断这个 shell 能不能用来登录。
func shellUsable(s string) bool {
	if s == "" {
		return false
	}
	switch path.Base(s) {
	case "nologin", "false", "sync", "halt", "shutdown":
		return false
	}
	return true
}

// probeLogin 探测容器能否用 root 密码登录。容器没运行时会顺带启动（不自动停）。
func probeLogin(name string) (*LoginStatus, error) {
	st := &LoginStatus{Issues: []string{}, Tips: []string{}}

	if c, _ := lxcInfo(name); c != nil {
		st.Running = c.State == "RUNNING"
	}
	if !st.Running {
		st.Issues = append(st.Issues, "容器没有运行，SSH 自然连不上")
		st.Tips = append(st.Tips, "先启动容器，再点「检查登录」")
		return st, nil
	}

	out, err := attachRun(name, loginProbeScript, "", 30*time.Second)
	kv := parseKV(out)
	if kv["done"] != "1" {
		msg := strings.TrimSpace(out)
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return st, fmt.Errorf("进不去容器（lxc-attach 失败）：%s", msg)
	}
	st.Reachable = true

	st.Shadow = kv["shadow"] == "1"
	hash := strings.TrimSpace(kv["hash"])
	if hash == "" {
		hash = strings.TrimSpace(kv["phash"])
	}
	st.Locked = strings.HasPrefix(hash, "!") || strings.HasPrefix(hash, "*")
	st.HashAlgo = hashAlgo(strings.TrimPrefix(strings.TrimPrefix(hash, "!"), "*"))
	st.HashSet = !st.Locked && hashUsable(hash)

	st.Shell = strings.TrimSpace(kv["shell"])
	st.ShellOK = shellUsable(st.Shell)

	st.SSHInstalled = strings.TrimSpace(kv["sshb"]) != ""
	st.SSHRunning = kv["sshrun"] == "1"
	st.SSHPort, _ = strconv.Atoi(strings.TrimSpace(kv["port"]))
	if st.SSHPort <= 0 {
		st.SSHPort = 22
	}
	listening := kv["listen"] != "" && kv["listen"] != "0"
	st.PermitRootLogin = strings.ToLower(strings.TrimSpace(kv["prl"]))
	st.PasswordAuth = strings.TrimSpace(kv["pwa"]) == "yes"

	st.Init = strings.TrimSpace(kv["init"])
	st.PkgMgr = strings.TrimSpace(kv["pkg"])
	st.Addr = strings.TrimSpace(kv["addr"])

	// ---- 逐项归因 ----
	if !st.Shadow {
		st.Issues = append(st.Issues, "容器里没有 /etc/shadow，用的是别的认证方式（如 /etc/passwd），chpasswd 可能不生效")
	}
	if st.Locked {
		st.Issues = append(st.Issues, "root 账号是锁定状态（/etc/shadow 里以 ! 开头），密码改了也会拒绝登录")
	} else if !st.HashSet {
		st.Issues = append(st.Issues, "root 没有可用的密码哈希（空密码或占位符），需要重新设置一次密码")
	}
	if !st.ShellOK {
		st.Issues = append(st.Issues, "root 的登录 shell 是「"+st.Shell+"」，不是正常 shell，登录后会被立刻踢出")
	}

	switch {
	case !st.SSHInstalled:
		st.NeedInstall = true
		st.Issues = append(st.Issues, "容器里没有 openssh-server，SSH 连接会被直接拒绝（这是 LXC 官方模板的默认状态）")
		st.Tips = append(st.Tips, "点「安装并开启 SSH」会自动安装（需要容器能出网，可用「一键修复出网」先修网络）")
	case !st.SSHRunning || !listening:
		st.Issues = append(st.Issues, "sshd 已经装了，但 "+strconv.Itoa(st.SSHPort)+" 端口上没有在监听（没启动或缺 host key）")
	}

	if st.SSHInstalled {
		switch st.PermitRootLogin {
		case "no":
			st.Issues = append(st.Issues, "sshd 配置为 PermitRootLogin no，完全禁止 root 登录")
		case "prohibit-password", "without-password", "forced-commands-only":
			st.Issues = append(st.Issues, "sshd 配置为 PermitRootLogin "+st.PermitRootLogin+"，root 只能密钥登录，密码怎么改都登不上")
		case "":
			// sshd -T 读不到，通常是 sshd 装得不完整，留在上面的启动问题里说
		}
		if !st.PasswordAuth {
			st.Issues = append(st.Issues, "sshd 关掉了 PasswordAuthentication，只接受密钥登录")
		}
	}

	st.Ready = st.HashSet && !st.Locked && st.ShellOK && st.SSHInstalled && st.SSHRunning &&
		listening && st.PasswordAuth && (st.PermitRootLogin == "yes")
	if st.Ready {
		st.Tips = append(st.Tips, "登录条件齐全：root 有密码、shell 正常、sshd 在监听且允许 root 密码登录")
		st.Tips = append(st.Tips, "在宿主机或局域网执行：ssh root@"+orDash(st.Addr)+" -p "+strconv.Itoa(st.SSHPort))
	}
	return st, nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "容器IP"
	}
	return s
}

// ---- 修复 ----

// LoginFix 是一次「修好登录」的结果。
type LoginFix struct {
	Logs        []string     `json:"logs"`
	NeedInstall bool         `json:"need_install"`
	Status      *LoginStatus `json:"status"`
}

const fixShellScript = `export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
CUR=$(grep '^root:' /etc/passwd 2>/dev/null | cut -d: -f7)
S=""
[ -x /bin/bash ] && S=/bin/bash
[ -x /bin/sh ] && S=/bin/sh
[ -z "$S" ] && { echo "找不到可用的 shell，放弃"; exit 1; }
case "$CUR" in
  ""|*/nologin|*/false|*/sync)
    if command -v usermod >/dev/null 2>&1; then
      usermod -s "$S" root
    else
      awk -F: -v s="$S" 'BEGIN{OFS=":"} $1=="root"{$7=s} {print}' /etc/passwd > /etc/passwd.lxcpanel && cat /etc/passwd.lxcpanel > /etc/passwd && rm -f /etc/passwd.lxcpanel
    fi
    echo "root 的登录 shell 已从「${CUR:-空}」改为 $S"
    ;;
  *) echo "root 的登录 shell 正常：$CUR" ;;
esac`

const fixSSHConfigScript = `export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
[ -x /usr/bin/ssh-keygen ] || [ -x /usr/sbin/ssh-keygen ] || command -v ssh-keygen >/dev/null 2>&1
ssh-keygen -A >/dev/null 2>&1 && echo "已确保 sshd host key 存在"
CHANGED=0
if grep -qE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d' /etc/ssh/sshd_config 2>/dev/null; then
  mkdir -p /etc/ssh/sshd_config.d
  printf 'PermitRootLogin yes\nPasswordAuthentication yes\n' > /etc/ssh/sshd_config.d/99-lxcpanel-login.conf
  echo "已写入 /etc/ssh/sshd_config.d/99-lxcpanel-login.conf（PermitRootLogin yes / PasswordAuthentication yes）"
  CHANGED=1
else
  sed -i -e 's/^[#[:space:]]*PermitRootLogin[[:space:]].*/PermitRootLogin yes/' -e 's/^[#[:space:]]*PasswordAuthentication[[:space:]].*/PasswordAuthentication yes/' /etc/ssh/sshd_config 2>/dev/null
  grep -q '^PermitRootLogin' /etc/ssh/sshd_config 2>/dev/null || echo 'PermitRootLogin yes' >> /etc/ssh/sshd_config
  grep -q '^PasswordAuthentication' /etc/ssh/sshd_config 2>/dev/null || echo 'PasswordAuthentication yes' >> /etc/ssh/sshd_config
  echo "已修改 /etc/ssh/sshd_config（PermitRootLogin yes / PasswordAuthentication yes）"
  CHANGED=1
fi
if sshd -t >/dev/null 2>&1; then
  echo "sshd 配置校验通过"
else
  [ -f /etc/ssh/sshd_config.d/99-lxcpanel-login.conf ] && rm -f /etc/ssh/sshd_config.d/99-lxcpanel-login.conf
  echo "sshd 配置校验失败，已回滚改动"
  exit 1
fi
OK=0
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl enable ssh >/dev/null 2>&1 || systemctl enable sshd >/dev/null 2>&1
  systemctl restart ssh >/dev/null 2>&1 || systemctl restart sshd >/dev/null 2>&1
  OK=1
fi
if [ "$OK" = "0" ] && command -v rc-service >/dev/null 2>&1; then
  rc-update add sshd default >/dev/null 2>&1
  rc-service sshd restart >/dev/null 2>&1
  OK=1
fi
if [ "$OK" = "0" ]; then
  sshd >/dev/null 2>&1 || /usr/sbin/sshd >/dev/null 2>&1
fi
sleep 1
if (pgrep -x sshd >/dev/null 2>&1) || ( (ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | grep -q ':22 ' ); then
  echo "sshd 已启动并在 22 端口监听"
else
  echo "sshd 仍未能启动，请查看容器日志（journalctl -u ssh）"
fi`

// fixLogin 修复登录链路上能自动修的部分。
// pass 是面板记录的 root 密码，用于解锁 / 重设；allowInstall 控制是否安装 openssh-server。
func fixLogin(name, pass string, allowInstall bool, logf func(string)) (*LoginFix, error) {
	log := func(s string) {
		if logf != nil {
			logf(s)
		}
	}
	res := &LoginFix{Logs: []string{}}
	push := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		res.Logs = append(res.Logs, s)
		log(s)
	}

	if c, _ := lxcInfo(name); c == nil || c.State != "RUNNING" {
		push("容器没有运行，先启动（SSH 需要容器在跑）")
		if out, err := containerAction(name, "start"); err != nil {
			return res, fmt.Errorf("启动容器失败: %s", strings.TrimSpace(out))
		}
		if err := waitContainerReady(name, 25*time.Second); err != nil {
			return res, err
		}
	}

	// 1) root 的 shell
	if out, err := attachRun(name, fixShellScript, "", 20*time.Second); err != nil {
		push("修复 root shell 失败：" + firstLine(out))
	} else {
		push(out)
	}

	// 2) 密码 / 锁定状态
	if h := rootHash(name); strings.HasPrefix(h, "!") || strings.HasPrefix(h, "*") || !hashUsable(h) {
		if validPassword(pass) == nil {
			push("root 处于锁定或无密码状态，重新写入面板记录的密码")
			if w, err := setRootPassword(name, pass, logf); err != nil {
				push("重设密码失败：" + err.Error())
			} else {
				push(w.Detail)
			}
		} else {
			push("root 处于锁定或无密码状态，但面板没有可用的密码记录，请先设置一次 root 密码")
		}
	} else {
		push("root 密码哈希正常（" + hashAlgo(h) + "）")
	}

	// 3) sshd
	st, _ := probeLogin(name)
	if st != nil && !st.SSHInstalled {
		res.NeedInstall = true
		if !allowInstall {
			push("容器里没有 openssh-server，需要先安装")
			res.Status = st
			return res, nil
		}
		push("开始安装 openssh-server（需要容器能访问软件源，可能要一两分钟）")
		if err := installSSH(name, push); err != nil {
			res.Status, _ = probeLogin(name)
			return res, err
		}
		push("openssh-server 安装完成")
	}

	// 4) sshd 配置与启动
	if out, err := attachRun(name, fixSSHConfigScript, "", 60*time.Second); err != nil {
		push("调整 sshd 配置失败：" + firstLine(out))
	} else {
		push(out)
	}

	st, err := probeLogin(name)
	res.Status = st
	return res, err
}

// installSSH 按发行版的包管理器安装 openssh-server。
func installSSH(name string, logf func(string)) error {
	kv := map[string]string{}
	if out, err := attachRun(name, "command -v apt-get >/dev/null 2>&1 && echo pkg=apt || (command -v apk >/dev/null 2>&1 && echo pkg=apk || (command -v dnf >/dev/null 2>&1 && echo pkg=dnf || (command -v yum >/dev/null 2>&1 && echo pkg=yum || (command -v zypper >/dev/null 2>&1 && echo pkg=zypper || echo pkg=none)))))", "", 15*time.Second); err == nil {
		kv = parseKV(out)
	}
	var script string
	switch kv["pkg"] {
	case "apt":
		script = "export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq openssh-server"
	case "apk":
		script = "apk add --no-cache openssh openssh-server 2>/dev/null || apk add --no-cache openssh"
	case "dnf":
		script = "dnf install -y openssh-server"
	case "yum":
		script = "yum install -y openssh-server"
	case "zypper":
		script = "zypper --non-interactive --gpg-auto-import-keys install -y openssh"
	default:
		return errors.New("容器里没有可用的包管理器（apt/apk/dnf/yum/zypper），请手动安装 openssh-server")
	}
	if logf != nil {
		logf("执行：" + script)
	}
	out, err := attachRun(name, "export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; "+script, "", 10*time.Minute)
	if err != nil {
		msg := strings.TrimSpace(out)
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("安装 openssh-server 失败：%s", msg)
	}
	return nil
}

// firstLine 取输出的第一行非空内容，用于紧凑的日志。
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}
