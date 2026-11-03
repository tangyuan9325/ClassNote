//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Windows 进程创建标志。
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	createNoWindow        = 0x08000000
)

// startHelper 启动辅助升级进程并与主程序彻底脱钩。
//
// 三个标志缺一不可：
//   - DETACHED_PROCESS：主程序退出时不连带把助手一起收走；
//   - CREATE_NEW_PROCESS_GROUP：控制台事件（Ctrl+C、关闭窗口）不会传下去；
//   - CREATE_NO_WINDOW：升级过程中不能在投影上弹出一个黑框。
func startHelper(exe, planPath, dir string) (int, error) {
	cmd := exec.Command(exe, ApplyFlag, planPath)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNewProcessGroup | detachedProcess | createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// waitExit 等待指定进程退出，超时返回 false。
//
// 不能再用 tasklist + find：那是按子串匹配的，PID 1234 会被
// 「find 123」当成命中，于是脚本永远在等一个早就退出的进程。
func waitExit(pid int, d time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// 打不开基本等于进程已经没了：没有句柄可等，直接放行。
		return true
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(d/time.Millisecond))
	if err != nil {
		return false
	}
	return ev == windows.WAIT_OBJECT_0
}

// alive 判断进程是否还活着。
func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return ev == uint32(windows.WAIT_TIMEOUT)
}

// relaunch 重新拉起主程序。
func relaunch(exe string, args []string, dir string) error {
	if _, err := os.Stat(exe); err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
	if err := cmd.Start(); err != nil {
		// 退一步交给 shell 启动：某些受限环境里直接 CreateProcess 会被拦，
		// 但 shell 的 start 能过。
		fallback := exec.Command("cmd", "/c", "start", "", exe)
		fallback.Args = append(fallback.Args, args...)
		fallback.Dir = dir
		fallback.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err2 := fallback.Start(); err2 != nil {
			return fmt.Errorf("%v（备用方式也失败：%v）", err, err2)
		}
		return nil
	}
	_ = cmd.Process.Release()
	return nil
}

// removeSelf 在进程退出后删除自己。
//
// 运行中的 exe 是被锁住的，只能借一个短命 cmd：先 ping 等两秒
// （比 timeout 稳，timeout 在无控制台时直接报错），再 del。
func removeSelf() {
	exe, err := os.Executable()
	if err != nil || !strings.Contains(strings.ToLower(filepath.Base(exe)), "updater-") {
		return
	}
	cmd := exec.Command("cmd", "/c", "ping -n 3 127.0.0.1 >nul & del /f /q \""+exe+"\"")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	_ = cmd.Start()
}
