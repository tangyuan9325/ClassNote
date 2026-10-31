package llm

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// BrowserProc 描述由本程序接管的浏览器进程。
type BrowserProc struct {
	Cmd  *exec.Cmd
	Port int
	URL  string // HTTP 调试地址
	WS   string // WebSocket 调试地址
}

func freePort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 9222
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// profilePort 由 profile 路径推导固定端口，便于复用同一个浏览器实例。
func profilePort(profileDir string) int {
	h := 0
	for _, c := range profileDir {
		h = (h*31 + int(c)) % 9973
	}
	if h < 0 {
		h = -h
	}
	return 9300 + h%60
}

// LaunchBrowser 启动带调试端口的 Chromium 内核浏览器，返回 CDP 地址。
// 自己启动可以避免 launcher 解析 stderr 失败的问题，也便于复用登录态目录。
func LaunchBrowser(bin, profileDir string, headless bool) (*BrowserProc, error) {
	if bin == "" {
		bin = findBrowser()
	}
	if bin == "" {
		return nil, fmt.Errorf("未找到 Edge / Chrome")
	}
	if profileDir == "" {
		profileDir = os.TempDir() + "\\classnote-webprofile"
	}
	_ = os.MkdirAll(profileDir, 0o755)
	port := profilePort(profileDir)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	// 已有实例在监听（上次未正常退出），直接复用，避免 profile 被锁。
	if ws := fetchWS(base); ws != "" {
		return &BrowserProc{Port: port, URL: base, WS: ws}, nil
	}
	if !portFree(port) {
		port = freePort()
	}
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		// 关掉一切可能抢老师屏幕的弹窗：翻译条、侧边栏、崩溃提示、更新提示。
		"--disable-features=Translate,msEdgeSidebarV2,CalculateNativeWinOcclusion",
		"--disable-infobars",
		"--disable-session-crashed-bubble",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-background-networking",
		"--disable-sync",
		"--hide-scrollbars",
		"--lang=zh-CN",
		"--window-size=1280,900",
		"about:blank",
	}
	if headless {
		// 无头之外再把窗口挪出屏幕，防止个别版本仍渲染窗口。
		args = append(args, "--window-position=-32000,-32000")
	}
	if headless {
		args = append([]string{"--headless=new", "--disable-gpu"}, args...)
	}
	cmd := exec.Command(bin, args...)
	if headless {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动浏览器失败: %w", err)
	}
	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		ws := fetchWS(base)
		if ws != "" {
			return &BrowserProc{Cmd: cmd, Port: port, URL: base, WS: ws}, nil
		}
		time.Sleep(220 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("浏览器调试端口未就绪（可能被安全策略拦截）")
}

// portFree 判断端口是否可监听。
func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// fetchWS 读取 /json/version 中的 WebSocket 调试地址。
func fetchWS(base string) string {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(base + "/json/version")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return ""
	}
	return v.WS
}

// killByPort 强制结束监听指定 TCP 端口的进程（Windows）。
// 用于清理复用失败的残留浏览器：它们没有 Cmd 句柄，只能按端口找到 PID 再结束。
func killByPort(port int) {
	if port <= 0 {
		return
	}
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return
	}
	needle := fmt.Sprintf(":%d", port)
	pid := ""
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 5 || !strings.EqualFold(f[3], "LISTENING") {
			continue
		}
		// 本地地址形如 127.0.0.1:9353
		if !strings.HasSuffix(f[1], needle) {
			continue
		}
		pid = f[len(f)-1]
		break
	}
	if pid == "" {
		return
	}
	_ = exec.Command("taskkill", "/F", "/PID", pid).Run()
}

// BrowserBinary 返回可用浏览器路径（导出给界面展示）。
func BrowserBinary() string { return findBrowser() }

// IsHeadlessSupported 简单判断浏览器是否支持无头模式（基于版本号）。
func IsHeadlessSupported(bin string) bool {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return false
	}
	v := strings.TrimSpace(string(out))
	return strings.Contains(v, "Edg/") || strings.Contains(v, "Chrome/")
}
