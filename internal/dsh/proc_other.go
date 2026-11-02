//go:build !windows

package dsh

import (
	"os"
	"os/exec"
	"syscall"
)

// hideWindow 在类 Unix 上没有窗口概念，空实现。
func hideWindow(cmd *exec.Cmd) {}

// setPgid 让子进程单独成为一个进程组。
//
// 这样 killTree 里的 kill(-pgid) 只会打到 DSH 自己那一组；
// 不设的话 pgid 与本进程相同，等于把自己的进程组一起送走。
func setPgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree 结束进程组：子进程（pnpm 等）要跟着一起走，否则端口一直被占。
func killTree(p *os.Process) {
	if pgid, err := syscall.Getpgid(p.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}
	_ = p.Signal(syscall.SIGTERM)
}
