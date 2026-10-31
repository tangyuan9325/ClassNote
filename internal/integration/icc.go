package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"classnote/internal/capture"
	"classnote/internal/config"
	"classnote/internal/logx"
)

// ICCState 描述 InkCanvasForClass 的联动状态。
type ICCState struct {
	Running         bool      `json:"running"`
	ExePath         string    `json:"exePath"`
	InstallDir      string    `json:"installDir"`
	PluginDir       string    `json:"pluginDir"`
	PluginSource    string    `json:"pluginSource"`
	PluginInstalled bool      `json:"pluginInstalled"`
	PluginVersion   string    `json:"pluginVersion"`
	Presenting      bool      `json:"presenting"`
	WindowTitle     string    `json:"windowTitle"`
	LastEvent       string    `json:"lastEvent"`
	LastEventAt     time.Time `json:"lastEventAt"`
	Injected        bool      `json:"injected"`
	Error           string    `json:"error,omitempty"`
}

// ICCBridge 管理与 ICC 的联动。
type ICCBridge struct {
	mu       sync.RWMutex
	cfg      config.ICCConfig
	state    ICCState
	lastPoll time.Time
}

// NewICCBridge 创建 ICC 联动器。
func NewICCBridge(cfg config.ICCConfig) *ICCBridge {
	return &ICCBridge{cfg: cfg}
}

// Update 更新配置。
func (b *ICCBridge) Update(cfg config.ICCConfig) {
	b.mu.Lock()
	b.cfg = cfg
	b.mu.Unlock()
}

// Event 记录来自 ICC 的事件（由桥接插件或脚本推送）。
func (b *ICCBridge) Event(name string, payload map[string]any) {
	b.mu.Lock()
	b.state.LastEvent = name
	b.state.LastEventAt = time.Now()
	if v, ok := payload["presenting"].(bool); ok {
		b.state.Presenting = v
	}
	b.mu.Unlock()
	logx.Infof("icc", "收到事件 %s", name)
}

// State 返回当前状态（带 1 秒缓存）。
func (b *ICCBridge) State() ICCState {
	b.mu.RLock()
	cfg := b.cfg
	cached := b.state
	last := b.lastPoll
	b.mu.RUnlock()

	if time.Since(last) < time.Second {
		return cached
	}

	st := ICCState{}
	proc, ok := capture.FindProcessInfo("InkCanvasForClass.exe", "InkCanvasForClass.CE.exe", "InkCanvasForClass")
	if ok {
		st.Running = true
		st.ExePath = proc.Path
		if proc.Path != "" {
			st.InstallDir = filepath.Dir(proc.Path)
		}
	}
	if st.InstallDir == "" && cfg.ExePath != "" {
		if fi, err := os.Stat(cfg.ExePath); err == nil && !fi.IsDir() {
			st.InstallDir = filepath.Dir(cfg.ExePath)
			st.ExePath = cfg.ExePath
		}
	}
	if st.InstallDir != "" {
		st.PluginDir = filepath.Join(st.InstallDir, "Plugins")
		if cfg.PluginDir != "" {
			st.PluginDir = cfg.PluginDir
		}
		st.PluginSource = filepath.Join(st.PluginDir, "ClassNote.Bridge.dll")
		if fi, err := os.Stat(st.PluginSource); err == nil {
			st.PluginInstalled = true
			st.PluginVersion = fi.ModTime().Format("2006-01-02")
		}
	}
	// 记录 ICC 窗口标题。放映状态以桥接插件推送的事件为准：
	// ICC 的主窗口本身就是全屏透明覆盖层，不能据此判断是否在放映。
	for _, w := range capture.ListWindows() {
		if !strings.EqualFold(w.Process, "InkCanvasForClass.exe") {
			continue
		}
		if w.Title != "" {
			st.WindowTitle = w.Title
		}
	}
	if !st.Running {
		st.Error = "未检测到 InkCanvasForClass 进程"
	}

	b.mu.Lock()
	// 插件推送的放映状态在 30 秒内视为权威，避免被本地窗口探测覆盖。
	if b.state.LastEvent != "" && time.Since(b.state.LastEventAt) < 30*time.Second {
		st.Presenting = b.state.Presenting
	}
	st.LastEvent = b.state.LastEvent
	st.LastEventAt = b.state.LastEventAt
	st.Injected = b.state.Injected
	b.state = st
	b.lastPoll = time.Now()
	b.mu.Unlock()
	return st
}

// CopyPlugin 把编译好的桥接插件复制到 ICC 的 Plugins 目录。
func (b *ICCBridge) CopyPlugin(src string) (ICCState, error) {
	b.mu.RLock()
	cfg := b.cfg
	b.mu.RUnlock()
	st := b.State()
	if st.PluginDir == "" {
		return st, fmt.Errorf("未找到 InkCanvasForClass 安装目录，请手动指定程序路径")
	}
	if src == "" {
		return st, fmt.Errorf("未找到桥接插件文件，请先编译 integrations/icc-bridge")
	}
	if _, err := os.Stat(src); err != nil {
		return st, fmt.Errorf("插件文件不存在: %s", src)
	}
	if err := os.MkdirAll(st.PluginDir, 0o755); err != nil {
		return st, err
	}
	dst := filepath.Join(st.PluginDir, "ClassNote.Bridge.dll")
	var copyErr error
	if Fingerprint(src) == Fingerprint(dst) && st.PluginInstalled {
		// 版本一致，无需覆盖（ICC 运行时还会占用该文件）。
		logx.Infof("icc", "桥接插件已是最新，跳过复制")
	} else if err := copyFile(src, dst); err != nil {
		copyErr = err
	}
	// 在 ICC 的插件配置里登记并启用，否则 ICC 只加载不启用。
	if err := setPluginEnabled(st.InstallDir, bridgePluginID, true); err != nil {
		logx.Warnf("icc", "写入插件启用状态失败: %v", err)
	}
	if copyErr != nil {
		if st.PluginInstalled {
			return b.State(), fmt.Errorf("插件已在运行中，配置已更新；如需升级 DLL 请先退出 ICC（%v）", copyErr)
		}
		return st, copyErr
	}
	// 同时写入一个说明文件，方便用户了解来源。
	_ = os.WriteFile(filepath.Join(st.PluginDir, "ClassNote.Bridge.README.txt"),
		[]byte("由 ClassNote 注入的联动插件。\n作用：把 ICC 的放映/翻页事件推送到 ClassNote 本地服务。\n卸载：删除 ClassNote.Bridge.dll 后重启 InkCanvasForClass。\n"), 0o644)
	b.mu.Lock()
	b.state.Injected = true
	b.mu.Unlock()
	_ = cfg
	logx.Infof("icc", "已注入桥接插件到 %s", dst)
	return b.State(), nil
}

// RemovePlugin 卸载桥接插件。
func (b *ICCBridge) RemovePlugin() (ICCState, error) {
	st := b.State()
	if st.PluginSource == "" {
		return st, fmt.Errorf("未找到插件路径")
	}
	if err := os.Remove(st.PluginSource); err != nil && !os.IsNotExist(err) {
		return st, err
	}
	_ = setPluginEnabled(st.InstallDir, bridgePluginID, false)
	b.mu.Lock()
	b.state.Injected = false
	b.mu.Unlock()
	return b.State(), nil
}

// FindLocalPlugin 在程序目录（含上两级，便于源码目录直接运行）查找随附的桥接 DLL。
func FindLocalPlugin(exeDir string) string {
	rel := []string{
		filepath.Join("plugins", "ClassNote.Bridge.dll"),
		"ClassNote.Bridge.dll",
		filepath.Join("integrations", "icc-bridge", "dist", "ClassNote.Bridge.dll"),
		filepath.Join("integrations", "icc-bridge", "bin", "Release", "ClassNote.Bridge.dll"),
	}
	dir := filepath.Clean(exeDir)
	for depth := 0; depth < 3; depth++ {
		for _, r := range rel {
			p := filepath.Join(dir, r)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// Fingerprint 返回文件摘要，用于判断插件是否需要更新。
func Fingerprint(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// bridgePluginID 是插件类的完整类型名，ICC 用它作为配置键。
const bridgePluginID = "ClassNote.Bridge.ClassNoteBridgePlugin"

// setPluginEnabled 修改 ICC 的 Configs/PluginConfig.json，控制插件是否随 ICC 启动。
func setPluginEnabled(iccDir, pluginID string, enabled bool) error {
	if iccDir == "" {
		return fmt.Errorf("未找到 ICC 目录")
	}
	path := filepath.Join(iccDir, "Configs", "PluginConfig.json")
	m := map[string]bool{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(stripBOM(raw), &m)
	}
	if enabled {
		m[pluginID] = true
	} else {
		delete(m, pluginID)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	raw, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	// ICC 的热重载可能瞬间占用文件，失败时重试几次。
	var lastErr error
	for i := 0; i < 5; i++ {
		if lastErr = os.WriteFile(dst, raw, 0o644); lastErr == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("写入插件失败（ICC 运行时可能被占用，请先退出 ICC）: %w", lastErr)
}
