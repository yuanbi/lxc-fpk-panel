package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

/* ------------------------------------------------------------------
   容器磁盘容量

   LXC 的 dir 存储与宿主机共用分区，没有容量上限；要限制磁盘就得让
   rootfs 落在一块独立设备上。最省事的是 LXC 自带的 loop 后端：

     lxc-create -B loop --fssize 8G ...
       → <容器目录>/rootdev   一块 ext4 镜像文件（容量即上限）
       → <容器目录>/rootfs    镜像的挂载点（停止时是空目录）
       → config: lxc.rootfs.path = loop:<容器目录>/rootdev

   面板在创建时可以直接选容量；对已有容器则做两种转换，都需要容器
   先停止：

     dir  → loop ：建镜像、格式化、复制数据、改写 rootfs.path
     loop → dir  ：挂载镜像、把数据复制回目录、删镜像、改回 dir:

   loop 镜像只能扩容（ext4 在线/离线都支持变大，缩小要重建文件系统，
   风险高，直接拒绝）。
------------------------------------------------------------------ */

const (
	gib           = int64(1024 * 1024 * 1024)
	diskImageName = "rootdev"
)

// loopStorageOK 判断宿主机是否具备 loop 存储能力（LXC 的 loop 后端依赖它）。
func loopStorageOK() bool {
	if _, err := os.Stat("/dev/loop-control"); err != nil {
		return false
	}
	for _, b := range []string{"mount", "mkfs.ext4"} {
		if !haveCmd(b) {
			return false
		}
	}
	return true
}

// containerDisk 返回容器的磁盘模式与镜像容量。
// dir 模式下容量为 0（表示不限制，由宿主机分区决定）。
func containerDisk(name string) (mode string, bytes int64) {
	backend, val := rootfsBackend(lxcPath(), name)
	if backend != "loop" {
		return "dir", 0
	}
	p := strings.TrimPrefix(val, "loop:")
	if p == "" {
		p = filepath.Join(lxcPath(), name, diskImageName)
	}
	if st, err := os.Stat(p); err == nil {
		return "loop", st.Size()
	}
	return "loop", 0
}

// ---------- 挂载辅助 ----------

func mountLoopImage(img, mnt string) error {
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return fmt.Errorf("创建挂载点失败: %v", err)
	}
	if out, err := runCmd(120*time.Second, "mount", "-o", "loop", img, mnt); err != nil {
		return fmt.Errorf("挂载镜像失败: %v %s", err, strings.TrimSpace(out))
	}
	return nil
}

func umountPath(mnt string) error {
	if out, err := runCmd(120*time.Second, "umount", mnt); err != nil {
		return fmt.Errorf("卸载 %s 失败: %v %s", mnt, err, strings.TrimSpace(out))
	}
	return nil
}

// isMounted 通过 /proc/mounts 判断挂载点是否仍被占用。
func isMounted(mnt string) bool {
	raw, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[1] == mnt {
			return true
		}
	}
	return false
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// e2fsck 修复过问题是正常情况，会以 1 / 2 退出，不能当成失败。
func e2fsckRecovered(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		c := ee.ExitCode()
		return c == 1 || c == 2
	}
	return false
}

// ---------- 调整容量 ----------

// resizeContainerDisk 设置容器磁盘上限。sizeGB 为 0 表示取消限制。
func resizeContainerDisk(name string, sizeGB int, logf func(string)) error {
	if sizeGB < 0 || sizeGB > 1024 {
		return errors.New("容量需在 0 - 1024 GB 之间（0 表示不限制）")
	}
	ctDir := filepath.Join(lxcPath(), name)
	if _, err := os.Stat(filepath.Join(ctDir, "config")); err != nil {
		return errors.New("容器不存在")
	}

	state := "UNKNOWN"
	if c, err := lxcInfo(name); err == nil && c.State != "" {
		state = c.State
	}
	if state != "STOPPED" {
		return fmt.Errorf("调整磁盘前请先停止容器（当前状态 %s）", state)
	}

	mode, cur := containerDisk(name)
	rootfs := filepath.Join(ctDir, "rootfs")
	img := filepath.Join(ctDir, diskImageName)

	if mode != "loop" {
		if sizeGB == 0 {
			logf("当前为目录存储：磁盘不限制，无需修改")
			return nil
		}
		return convertDirToLoop(name, ctDir, rootfs, img, sizeGB, logf)
	}
	if sizeGB == 0 {
		return convertLoopToDir(name, ctDir, rootfs, img, logf)
	}

	want := int64(sizeGB) * gib
	if want <= cur {
		return fmt.Errorf("loop 镜像只能扩容：当前 %s，目标 %s", fmtSize(cur), fmtSize(want))
	}
	if !loopStorageOK() {
		return errors.New("当前系统不支持 loop 存储")
	}
	logf(fmt.Sprintf("扩容镜像：%s → %s", fmtSize(cur), fmtSize(want)))
	if err := os.Truncate(img, want); err != nil {
		return fmt.Errorf("扩展镜像文件失败: %v", err)
	}
	logf("镜像文件已扩展，正在检查文件系统（可能需要几分钟）...")
	if out, err := runCmd(600*time.Second, "e2fsck", "-f", "-y", img); err != nil {
		if !e2fsckRecovered(err) {
			return fmt.Errorf("e2fsck 检查失败: %v %s", err, lastLines(out, 3))
		}
		logf("e2fsck 修复了文件系统问题，继续扩容")
	}
	if out, err := runCmd(600*time.Second, "resize2fs", img); err != nil {
		return fmt.Errorf("resize2fs 扩容失败: %v %s", err, lastLines(out, 3))
	}
	logf("完成：文件系统已扩展到 " + fmtSize(want))
	return nil
}

// convertDirToLoop 把目录存储的容器转成 loop 镜像存储，从而限制容量。
func convertDirToLoop(name, ctDir, rootfs, img string, sizeGB int, logf func(string)) error {
	if !loopStorageOK() {
		return errors.New("当前系统不支持 loop 存储（缺少 /dev/loop-control 或 mount）")
	}
	if isMounted(rootfs) {
		return errors.New("rootfs 仍处于挂载状态，请确认容器已完全停止")
	}
	used := dirSize(rootfs)
	want := int64(sizeGB) * gib
	need := int64(float64(used) * 1.15)
	if used > 0 && want < need {
		return fmt.Errorf("容量过小：现有数据约 %s，至少需要 %s", fmtSize(used), fmtSize(need))
	}
	_, free := usageOf(ctDir)
	if used > 0 && uint64(need) > free {
		return fmt.Errorf("容器所在分区可用空间不足，至少需要 %s", fmtSize(need))
	}
	bak := filepath.Join(ctDir, "rootfs.dirbak")
	for _, p := range []string{img, bak} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s 已存在，请先清理后再试", p)
		}
	}

	logf(fmt.Sprintf("创建 %s 的 ext4 镜像（%d GB）", name, sizeGB))
	f, err := os.OpenFile(img, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建镜像文件失败: %v", err)
	}
	if err := f.Truncate(want); err != nil {
		f.Close()
		os.Remove(img)
		return fmt.Errorf("分配镜像空间失败: %v", err)
	}
	f.Close()
	if out, err := runCmd(300*time.Second, "mkfs.ext4", "-F", "-q", img); err != nil {
		os.Remove(img)
		return fmt.Errorf("格式化镜像失败: %v %s", err, lastLines(out, 3))
	}

	logf("把现有 rootfs（约 " + fmtSize(used) + "）搬进镜像 ...")
	if err := os.Rename(rootfs, bak); err != nil {
		os.Remove(img)
		return fmt.Errorf("备份原 rootfs 失败: %v", err)
	}
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		os.Rename(bak, rootfs)
		os.Remove(img)
		return fmt.Errorf("创建挂载点失败: %v", err)
	}
	restore := func() {
		if isMounted(rootfs) {
			umountPath(rootfs)
		}
		os.RemoveAll(rootfs)
		if err := os.Rename(bak, rootfs); err != nil {
			return
		}
		os.Remove(img)
	}
	if err := mountLoopImage(img, rootfs); err != nil {
		restore()
		return err
	}
	if err := runStream(logf, "cp", "-a", bak+"/.", rootfs+"/"); err != nil {
		restore()
		return fmt.Errorf("复制数据到镜像失败: %v", err)
	}
	if err := umountPath(rootfs); err != nil {
		// 数据已经写入镜像，但没能卸载；保留 rootfs.dirbak 供人工处理。
		return err
	}

	// 校验镜像里确实有内容，再改写配置。
	probe := filepath.Join(ctDir, ".rootfs-probe")
	os.RemoveAll(probe)
	ok := false
	if err := mountLoopImage(img, probe); err == nil {
		if _, err := os.Stat(filepath.Join(probe, "etc")); err == nil {
			ok = true
		}
		umountPath(probe)
	}
	os.RemoveAll(probe)
	if !ok {
		restore()
		return errors.New("镜像校验失败（未找到 etc 目录），已回滚到目录存储")
	}

	if err := setConfigKey(name, "lxc.rootfs.path", "loop:"+img); err != nil {
		restore()
		return fmt.Errorf("改写容器配置失败: %v", err)
	}
	if err := os.RemoveAll(bak); err != nil {
		logf("清理备份 " + bak + " 失败: " + err.Error())
	}
	logf(fmt.Sprintf("完成：磁盘已限制为 %d GB（镜像 %s）", sizeGB, img))
	logf("提示：容器启动时会自动挂载该镜像，容器内看到的根分区即为该容量")
	return nil
}

// convertLoopToDir 取消容量限制：把镜像内容搬回普通目录并删除镜像。
func convertLoopToDir(name, ctDir, rootfs, img string, logf func(string)) error {
	if _, err := os.Stat(img); err != nil {
		return fmt.Errorf("找不到镜像文件 %s", img)
	}
	tmp := filepath.Join(ctDir, ".rootfs-copy")
	os.RemoveAll(tmp)
	if err := mountLoopImage(img, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	used := dirSize(tmp)
	if err := umountPath(tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	_, free := usageOf(ctDir)
	need := int64(float64(used) * 1.15)
	if uint64(need) > free {
		os.RemoveAll(tmp)
		return fmt.Errorf("容器所在分区可用空间不足，至少需要 %s", fmtSize(need))
	}

	logf(fmt.Sprintf("把镜像内容（约 %s）复制回目录 ...", fmtSize(used)))
	if err := mountLoopImage(img, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	defer func() {
		if isMounted(tmp) {
			umountPath(tmp)
		}
		os.RemoveAll(tmp)
	}()

	if isMounted(rootfs) {
		if err := umountPath(rootfs); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return fmt.Errorf("创建 rootfs 目录失败: %v", err)
	}
	// 挂载点目录此时应为空；若有残留先清掉，避免新旧文件混在一起。
	if entries, err := os.ReadDir(rootfs); err == nil {
		for _, e := range entries {
			os.RemoveAll(filepath.Join(rootfs, e.Name()))
		}
	}
	if err := runStream(logf, "cp", "-a", tmp+"/.", rootfs+"/"); err != nil {
		return fmt.Errorf("复制数据失败: %v", err)
	}
	if err := umountPath(tmp); err != nil {
		return err
	}
	if err := setConfigKey(name, "lxc.rootfs.path", "dir:"+rootfs); err != nil {
		return fmt.Errorf("改写容器配置失败: %v", err)
	}
	if err := os.Remove(img); err != nil {
		logf("删除镜像 " + img + " 失败: " + err.Error())
	}
	logf("完成：已转换为目录存储，磁盘不再限制（随宿主机分区）")
	return nil
}
