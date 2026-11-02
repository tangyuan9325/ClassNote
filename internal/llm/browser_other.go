//go:build !windows

package llm

import "os/exec"

// hideWindow 在非 Windows 平台上无事可做。
// 单独拆文件是为了让 llm 包能在 Linux 上编译，静态检查与单测跑得起来。
func hideWindow(cmd *exec.Cmd) {}
