//go:build !windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// startHelper 启动辅助升级进程。
//
// Setsid 让它成为独立会话的首进程：主程序退出（或被关闭终端）时
// 不会连带把它 SIGINT 掉。
func startHelper(exe, planPath, dir string) (int, error) {
	cmd := exec.Command(exe, ApplyFlag, planPath)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// waitExit 轮询等待进程退出。
func waitExit(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return !alive(pid)
}

// alive 判断进程是否还活着。
//
// 优先看 /proc：父进程还没回收的僵尸进程对 signal 0 是「活着」的，
// 但文件早就关了，等它没有任何意义。
func alive(pid int) bool {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 2 && fields[2] == "Z" {
			return false
		}
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// relaunch 重新拉起主程序。
func relaunch(exe string, args []string, dir string) error {
	if _, err := os.Stat(exe); err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%v", err)
	}
	_ = cmd.Process.Release()
	return nil
}

// removeSelf 直接删除自己：类 Unix 下运行中的可执行文件可以 unlink。
func removeSelf() {
	exe, err := os.Executable()
	if err != nil || !strings.Contains(filepath.Base(exe), "updater-") {
		return
	}
	_ = os.Remove(exe)
}
