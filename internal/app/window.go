package app

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"classnote/internal/logx"
)

// BrowserPath 返回可用的 Chromium 内核浏览器路径。
func BrowserPath() string {
	cands := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// LaunchWindow 以独立应用窗口打开界面（无地址栏，接近原生应用）。
func LaunchWindow(url, profileDir string, w, h int) error {
	bin := BrowserPath()
	if bin == "" {
		return fmt.Errorf("未找到 Edge / Chrome，请手动在浏览器打开 %s", url)
	}
	_ = os.MkdirAll(profileDir, 0o755)
	if w <= 0 {
		w = 1380
	}
	if h <= 0 {
		h = 900
	}
	args := []string{
		"--app=" + url,
		"--user-data-dir=" + profileDir,
		"--window-size=" + fmt.Sprintf("%d,%d", w, h),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,msEdgeSidebarV2",
		"--disable-background-networking",
		"--disable-sync",
	}
	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动界面窗口失败: %w", err)
	}
	logx.Infof("app", "界面窗口已启动 pid=%d", cmd.Process.Pid)
	go func() {
		_ = cmd.Wait()
		logx.Infof("app", "界面窗口已关闭")
	}()
	return nil
}

// OpenFolder 在资源管理器中打开目录（不存在时先创建）。
func OpenFolder(dir string) error {
	if dir == "" {
		return fmt.Errorf("目录为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("explorer.exe", dir)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("打开目录失败: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// OpenExternal 用默认浏览器打开链接。
func OpenExternal(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// IsRunning 检查端口上是否已经是本应用实例。
func IsRunning(url string) bool {
	client := &http.Client{Timeout: 900 * time.Millisecond}
	resp, err := client.Get(url + "/api/bootstrap")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 && strings.Contains(resp.Header.Get("Content-Type"), "json")
}
