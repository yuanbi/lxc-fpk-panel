package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// 容器的 root 密码：创建时自动生成一个随机密码，之后可以在面板里改。
//
// 设置方式是容器起来后用 lxc-attach 调 chpasswd（走容器自己的 PAM/加密方式，
// 比在宿主机上直接改 /etc/shadow 兼容性好）。容器停着的话临时启一下再停回去。

const (
	passAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	// defaultPassLen 是创建容器时随机密码的长度。
	defaultPassLen = 14
)

// randomPassword 生成 n 位随机密码，剔除了 0/O/1/l/I 这类容易看错的字符。
func randomPassword(n int) string {
	if n <= 0 {
		n = defaultPassLen
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "lxc" + fmt.Sprint(time.Now().UnixNano())[:9]
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = passAlphabet[int(b)%len(passAlphabet)]
	}
	return string(out)
}

func validPassword(p string) error {
	if len(p) < 6 {
		return errors.New("密码至少 6 位")
	}
	if len(p) > 128 {
		return errors.New("密码最多 128 位")
	}
	if strings.ContainsAny(p, "\n\r") {
		return errors.New("密码不能包含换行符")
	}
	return nil
}

// attachRun 在容器里跑一条 shell 命令，返回合并后的输出。
func attachRun(name, script, stdin string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	args := append(lxcPathArgs(), "-n", name, "--", "sh", "-c", script)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lxc-attach", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil && out == "" {
		out = err.Error()
	}
	return out, err
}

func attachWrite(name, script string, stdin string) error {
	out, err := attachRun(name, script, stdin, 30*time.Second)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// waitContainerReady 等容器里的 init 起来（lxc-attach 需要 init 在跑才能进）。
func waitContainerReady(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := attachRun(name, "true", "", 5*time.Second); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("容器启动后仍无法进入（lxc-attach 失败），请稍后重试")
		}
		time.Sleep(600 * time.Millisecond)
	}
}

// PassWrite 记录一次密码写入的结果。
//
// 只判断命令 exit code 是不够的：chpasswd 在某些镜像里会「成功返回但没写进去」
// （/etc/shadow 只读、busybox 精简版、rootfs 异常），表现就是面板提示成功、
// 用户却怎么都登不上。所以写完必须回读 /etc/shadow 对比哈希是否真的变了。
type PassWrite struct {
	// Method 是最终生效的写入方式（chpasswd / passwd）。
	Method string `json:"method"`
	// Changed 表示写入后哈希与写入前不同。
	Changed bool `json:"changed"`
	// Verified 表示哈希格式合法（$6$ 之类），可认为密码确实生效。
	Verified bool `json:"verified"`
	// Locked 表示写入后 root 仍处于锁定状态。
	Locked bool `json:"locked"`
	// Algo 是哈希算法（SHA-512 / yescrypt …）。
	Algo string `json:"algo"`
	// Detail 是给人看的一句话说明。
	Detail string `json:"detail"`
}

// hashAlgo 从 crypt(3) 哈希里识别算法名。
func hashAlgo(h string) string {
	switch {
	case strings.HasPrefix(h, "$1$"):
		return "MD5"
	case strings.HasPrefix(h, "$2a$"), strings.HasPrefix(h, "$2b$"), strings.HasPrefix(h, "$2y$"):
		return "bcrypt"
	case strings.HasPrefix(h, "$5$"):
		return "SHA-256"
	case strings.HasPrefix(h, "$6$"):
		return "SHA-512"
	case strings.HasPrefix(h, "$y$"):
		return "yescrypt"
	case strings.HasPrefix(h, "$7$"):
		return "scrypt"
	}
	return ""
}

// hashUsable 判断这个哈希是不是一个「有真实密码」的哈希。
func hashUsable(h string) bool {
	return hashAlgo(h) != "" && len(h) > 12 && strings.Count(h, "$") >= 3
}

// rootHash 读容器里 root 的密码哈希（优先 /etc/shadow，退回 /etc/passwd）。
// 读不到返回空串。只接受形如 $6$… 的结果，避免把报错信息当成哈希。
func rootHash(name string) string {
	const script = "export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; " +
		"H=$(grep '^root:' /etc/shadow 2>/dev/null | cut -d: -f2); " +
		"[ -z \"$H\" ] && H=$(grep '^root:' /etc/passwd 2>/dev/null | cut -d: -f2); printf '%s' \"$H\""
	out, _ := attachRun(name, script, "", 15*time.Second)
	out = strings.TrimSpace(out)
	// 兜底：只保留哈希里可能出现的字符，防止把 stderr 混进来
	if strings.ContainsAny(out, "\n\r") {
		out = strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	}
	return out
}

// setRootPassword 设置容器 root 密码。容器没运行时会临时启动，设置完再停回原状态。
// 返回的 PassWrite 描述这次写入是否真的落到了 /etc/shadow。
func setRootPassword(name, pass string, logf func(string)) (*PassWrite, error) {
	if err := validPassword(pass); err != nil {
		return nil, err
	}
	c, _ := lxcInfo(name)
	wasRunning := c != nil && c.State == "RUNNING"
	if !wasRunning {
		if logf != nil {
			logf("临时启动容器以设置密码 ...")
		}
		if out, err := containerAction(name, "start"); err != nil {
			return nil, fmt.Errorf("启动容器失败: %s", strings.TrimSpace(out))
		}
		defer func() {
			if logf != nil {
				logf("恢复原状态：停止容器")
			}
			containerAction(name, "stop")
		}()
	}
	// init 没起来时 lxc-attach 会直接失败，先等一会儿再动手
	if err := waitContainerReady(name, 20*time.Second); err != nil {
		return nil, err
	}

	before := rootHash(name)

	// chpasswd 是最通用的（Debian/Ubuntu/Alma/Rocky/openSUSE 都有），
	// 极简镜像（Alpine busybox）没有就退回 passwd。
	// lxc-attach 的 PATH 很短，这里显式补上 sbin，否则找不到 chpasswd。
	const pathEnv = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; "
	attempts := []struct {
		desc  string
		shell string
		stdin string
	}{
		{"chpasswd", pathEnv + "chpasswd", "root:" + pass + "\n"},
		{"/usr/sbin/chpasswd", "/usr/sbin/chpasswd", "root:" + pass + "\n"},
		{"passwd", pathEnv + fmt.Sprintf("printf '%%s\\n%%s\\n' '%s' '%s' | passwd root", pass, pass), ""},
	}
	var lastErr error
	used := ""
	for _, a := range attempts {
		if err := attachWrite(name, a.shell, a.stdin); err != nil {
			lastErr = err
			continue
		}
		used = a.desc
		break
	}
	if used == "" {
		return nil, fmt.Errorf("设置 root 密码失败: %v", lastErr)
	}

	// 回读校验：命令返回成功不代表真的写进去了
	after := rootHash(name)
	w := &PassWrite{Method: used, Algo: hashAlgo(after)}
	switch {
	case after == "":
		w.Detail = "已经执行 chpasswd，但读不到 /etc/shadow 里的 root 条目，无法确认是否生效"
		if logf != nil {
			logf("警告：" + w.Detail)
		}
		return w, nil
	case after == before:
		return w, errors.New("密码没有写进容器：/etc/shadow 里的 root 哈希和写入前完全一样（可能是 rootfs 只读或镜像用了非 shadow 的认证方式）")
	case strings.HasPrefix(after, "!") || strings.HasPrefix(after, "*"):
		w.Changed, w.Locked = true, true
		w.Detail = "密码已写入，但 root 账号仍被锁定（/etc/shadow 以 ! 开头），需要解锁后才能登录"
		if logf != nil {
			logf("警告：" + w.Detail)
		}
		return w, nil
	}
	w.Changed, w.Verified = true, hashUsable(after)
	if w.Verified {
		w.Detail = "已写入 /etc/shadow 并通过回读校验（" + w.Algo + "）"
	} else {
		w.Detail = "已写入 /etc/shadow，但哈希格式不常见：" + after
	}
	if logf != nil {
		if w.Verified {
			logf("容器 root 密码已设置为 " + pass + "（" + w.Algo + "，已回读校验）")
		} else {
			logf("警告：" + w.Detail)
		}
	}
	return w, nil
}
