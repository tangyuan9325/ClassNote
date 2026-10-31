package integration

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets/classisland-bridge.cnx
var classIslandPluginPack []byte

// PluginState 是 ClassIsland 注入插件返回的课堂状态。
type PluginState struct {
	OK         bool   `json:"ok"`
	Host       string `json:"host"`
	Version    string `json:"version"`
	UpdatedAt  string `json:"updatedAt"`
	InClass    bool   `json:"inClass"`
	Preparing  bool   `json:"preparing"`
	Subject    string `json:"subject"`
	ClassName  string `json:"className"`
	Start      string `json:"start"`
	End        string `json:"end"`
	Next       string `json:"next"`
	NextStart  string `json:"nextStart"`
	TimeState  string `json:"timeState"`
	TodayCount int    `json:"todayCount"`
	Error      string `json:"error,omitempty"`
}

// PluginInfo 描述 ClassIsland 注入插件的安装与连通状态。
type PluginInfo struct {
	Installed  bool         `json:"installed"`
	Running    bool         `json:"running"`
	Version    string       `json:"version,omitempty"`
	PluginsDir string       `json:"pluginsDir"`
	Port       int          `json:"port,omitempty"`
	State      *PluginState `json:"state,omitempty"`
	Error      string       `json:"error,omitempty"`
}

func (p *ClassIslandProvider) pluginDir() string {
	dir := p.DataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "Plugins", "classnote.islandbridge")
}

// PluginInfo 探测插件是否安装、ClassIsland 是否已加载。
func (p *ClassIslandProvider) PluginInfo() PluginInfo {
	dir := p.pluginDir()
	info := PluginInfo{}
	if dir == "" {
		info.Error = "未找到 ClassIsland 数据目录"
		return info
	}
	info.PluginsDir = dir
	if st, err := os.Stat(filepath.Join(dir, "manifest.yml")); err == nil && !st.IsDir() {
		info.Installed = true
		info.Version = pluginVersion(filepath.Join(dir, "manifest.yml"))
	}
	st, err := p.probePlugin(true)
	if err == nil {
		info.Running = true
		info.Port = p.pluginPort
		info.State = &st
	} else if info.Installed {
		info.Error = err.Error()
	}
	return info
}

// InstallPlugin 把随包携带的插件解压到 ClassIsland 插件目录。
func (p *ClassIslandProvider) InstallPlugin() (PluginInfo, error) {
	dir := p.pluginDir()
	if dir == "" {
		return PluginInfo{}, fmt.Errorf("未找到 ClassIsland 数据目录，请先在设置里指定 ClassIsland.exe")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return PluginInfo{}, err
	}
	zr, err := zip.NewReader(bytes.NewReader(classIslandPluginPack), int64(len(classIslandPluginPack)))
	if err != nil {
		return PluginInfo{}, fmt.Errorf("内置插件包损坏: %w", err)
	}
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		dst := filepath.Join(dir, name)
		if !strings.HasPrefix(dst, dir+string(os.PathSeparator)) && dst != dir {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(dst, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return PluginInfo{}, err
		}
		rc, err := f.Open()
		if err != nil {
			return PluginInfo{}, err
		}
		raw, err := io.ReadAll(io.LimitReader(rc, 8<<20))
		rc.Close()
		if err != nil {
			return PluginInfo{}, err
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "cannot access") || os.IsPermission(err) {
				return PluginInfo{}, fmt.Errorf("插件正在被 ClassIsland 使用，请先退出 ClassIsland 再安装或更新")
			}
			return PluginInfo{}, err
		}
	}
	// 清掉旧端口缓存，避免读到上一次运行的值。
	_ = os.Remove(filepath.Join(dir, "port.txt"))
	if data := p.DataDir(); data != "" {
		_ = os.Remove(filepath.Join(data, "Config", "Plugins", "classnote.islandbridge", "port.txt"))
	}
	return p.PluginInfo(), nil
}

// UninstallPlugin 移除插件目录。
func (p *ClassIslandProvider) UninstallPlugin() error {
	dir := p.pluginDir()
	dataDir := p.DataDir()
	if dir == "" || dataDir == "" {
		return fmt.Errorf("未找到 ClassIsland 插件目录")
	}
	if !strings.HasPrefix(dir, dataDir+string(os.PathSeparator)) {
		return fmt.Errorf("插件目录不在 ClassIsland 数据目录内，已拒绝删除")
	}
	_ = os.RemoveAll(filepath.Join(dataDir, "Config", "Plugins", "classnote.islandbridge"))
	return os.RemoveAll(dir)
}

// NotifyPlugin 让 ClassIsland 弹出课堂提醒。
func (p *ClassIslandProvider) NotifyPlugin(title, body string) error {
	port := p.readPluginPort()
	if port == 0 {
		return fmt.Errorf("ClassIsland 插件未运行")
	}
	payload, _ := json.Marshal(map[string]string{"title": title, "body": body})
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/notify", port), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// 通知会让 ClassIsland 走一遍完整的展示流程，给足时间，不要用状态探测的短超时。
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连接 ClassIsland 插件失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var out struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.OK && out.Result != "queued" {
		return fmt.Errorf("ClassIsland 未接受通知: %s", out.Result)
	}
	return nil
}

func (p *ClassIslandProvider) readPluginPort() int {
	dirs := []string{}
	if dir := p.pluginDir(); dir != "" {
		dirs = append(dirs, dir)
	}
	// ClassIsland 会把插件配置目录放在 data/Config/Plugins/<id>。
	if data := p.DataDir(); data != "" {
		dirs = append(dirs, filepath.Join(data, "Config", "Plugins", "classnote.islandbridge"))
	}
	for _, dir := range dirs {
		raw, err := os.ReadFile(filepath.Join(dir, "port.txt"))
		if err != nil {
			continue
		}
		var port int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &port); err != nil {
			continue
		}
		if port >= 1024 && port <= 65535 {
			return port
		}
	}
	return 0
}

// probePlugin 拉取插件状态（带 2 秒缓存）。
func (p *ClassIslandProvider) probePlugin(force bool) (PluginState, error) {
	p.mu.Lock()
	last, at, ok, port := p.pluginLast, p.pluginAt, p.pluginOK, p.pluginPort
	p.mu.Unlock()
	if !force && ok && time.Since(at) < 2*time.Second {
		return last, nil
	}
	port = p.readPluginPort()
	if port == 0 {
		return PluginState{}, fmt.Errorf("插件尚未启动（安装后需要重启 ClassIsland）")
	}
	client := p.pluginClient
	if client == nil {
		client = &http.Client{Timeout: 700 * time.Millisecond}
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/state", port))
	if err != nil {
		return PluginState{}, fmt.Errorf("插件未响应: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var st PluginState
	if err := json.Unmarshal(raw, &st); err != nil {
		return PluginState{}, fmt.Errorf("插件返回格式异常: %w", err)
	}
	if !st.OK {
		return PluginState{}, fmt.Errorf("插件状态异常")
	}
	p.mu.Lock()
	p.pluginLast, p.pluginAt, p.pluginOK, p.pluginPort = st, time.Now(), true, port
	p.mu.Unlock()
	return st, nil
}

func (p *ClassIslandProvider) pluginSnapshot() (PluginState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pluginOK && time.Since(p.pluginAt) < 5*time.Second {
		return p.pluginLast, true
	}
	return PluginState{}, false
}

func pluginVersion(manifest string) string {
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "version:"))
		}
	}
	return ""
}
