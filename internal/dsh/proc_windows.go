//go:build windows

package dsh

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// hideWindow 让 node/npm 子进程不弹黑框。
// 课堂软件弹一个命令行窗口出来是最伤体验的事之一。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// setPgid 在 Windows 上没有进程组语义，taskkill /T 已经能带走子树。
func setPgid(cmd *exec.Cmd) {}

// killTree 结束整个进程树。
// DSH 会 fork 出 pnpm 与若干子进程，只杀父进程会留下孤儿占着端口。
func killTree(p *os.Process) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
	_ = p.Kill()
}
