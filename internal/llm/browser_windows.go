//go:build windows

package llm

import (
	"os/exec"
	"syscall"
)

// hideWindow 让无头浏览器不闪一个黑框。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
