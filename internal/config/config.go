// Package config 负责应用配置的定义、默认值、加载与持久化。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Config 是应用的全部可持久化配置。
type Config struct {
	Version   int                `json:"version"`
	Onboarded bool               `json:"onboarded"`
	Provider  ProviderConfig     `json:"provider"`
	WebAI     WebAIConfig        `json:"webAI"`
	ASR       ASRConfig          `json:"asr"`
	Capture   CaptureConfig      `json:"capture"`
	Lesson    LessonConfig       `json:"lesson"`
	Integ     IntegrationsConfig `json:"integrations"`
	UI        UIConfig           `json:"ui"`
	Server    ServerConfig       `json:"server"`
	Notify    NotifyConfig       `json:"notify"`
	GitHub    GitHubConfig       `json:"github"`
	Agent     AgentConfig        `json:"agent"`
	LastRun   time.Time          `json:"lastRun"`
}

// GitHubConfig 控制更新检查与仓库信息。
type GitHubConfig struct {
	// Repo 形如 "tangyuan9325/ClassNote"
	Repo         string `json:"repo"`
	Token        string `json:"token,omitempty"`
	CheckUpdates bool   `json:"checkUpdates"`
}

// NotifyConfig 控制机器人通知。
type NotifyConfig struct {
	Enabled   bool             `json:"enabled"`
	Providers []NotifyProvider `json:"providers"`
}

// NotifyProvider 一个通知渠道。
type NotifyProvider struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"` // wecom | dingtalk | feishu | telegram | serverchan | webhook
	Enabled bool     `json:"enabled"`
	Webhook string   `json:"webhook,omitempty"`
	Secret  string   `json:"secret,omitempty"`
	Token   string   `json:"token,omitempty"`
	ChatID  string   `json:"chatId,omitempty"`
	Events  []string `json:"events,omitempty"` // info / warn / error / *，空表示全部
}

// ServerConfig 控制局域网访问与访问密钥。
type ServerConfig struct {
	// Access: local 只监听本机（默认）；lan 监听局域网（需要密钥）
	Access string `json:"access"`
	// Token 访问密钥，首次启动随机生成，用户修改后持久保存
	Token string `json:"token"`
}

// ProviderConfig 描述大模型 API 接入方式。
type ProviderConfig struct {
	Mode        string            `json:"mode"`     // api | web
	Preset      string            `json:"preset"`   // deepseek / qwen / kimi / zhipu / openai / siliconflow / ollama / custom
	BaseURL     string            `json:"baseURL"`  // OpenAI 兼容或 Anthropic 的 base url
	APIKey      string            `json:"apiKey"`   //
	Model       string            `json:"model"`    //
	Protocol    string            `json:"protocol"` // openai | anthropic
	Temperature float64           `json:"temperature"`
	MaxTokens   int               `json:"maxTokens"`
	Vision      bool              `json:"vision"` // 模型是否支持图片输入
	TimeoutSec  int               `json:"timeoutSec"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// WebAIConfig 描述网页版 AI 的浏览器自动化参数。
type WebAIConfig struct {
	Site         string            `json:"site"`        // qwen | deepseek | custom
	URL          string            `json:"url"`         //
	BrowserPath  string            `json:"browserPath"` // 空则自动探测 Edge/Chrome
	ProfileDir   string            `json:"profileDir"`  // 便于保留登录态
	Headless     bool              `json:"headless"`
	LoggedIn     bool              `json:"loggedIn"`
	Selectors    map[string]string `json:"selectors,omitempty"`
	ReplyTimeout int               `json:"replyTimeoutSec"`
	// ChatsFile 是「每门课一条固定对话」的记录文件，由程序在启动时填成
	// <数据目录>/webai-chats.json，不写进配置文件。
	ChatsFile string `json:"-"`
	// ChatMode 决定网页版怎么用对话：
	//   per-subject（默认）每门课固定一条对话，系统提示只写一次；
	//   per-page 每页新开对话（旧行为，页面之间不串上下文）。
	ChatMode string `json:"chatMode,omitempty"`
	// NoPrimeOnStart 取反语义：零值即「启动时自动准备对话」。
	// 用 PrimeOnStart 的话，老配置文件里没有这个字段，升级后会被当成关闭，
	// 正好和「配好就能用」的预期相反。
	NoPrimeOnStart bool `json:"noPrimeOnStart,omitempty"`
}

// ASRConfig 描述语音转写参数。
type ASRConfig struct {
	Mode       string  `json:"mode"` // api | local | off
	BaseURL    string  `json:"baseURL"`
	APIKey     string  `json:"apiKey"`
	Model      string  `json:"model"`
	Language   string  `json:"language"`
	LocalExe   string  `json:"localExe"`
	LocalModel string  `json:"localModel"`
	LocalPort  int     `json:"localPort"`  // 本地 whisper-server 端口（0 = 默认 18095）
	ChunkSec   int     `json:"chunkSec"`   // 单段最长秒数
	SilenceMS  int     `json:"silenceMs"`  // 静音判定时长
	Threshold  float64 `json:"threshold"`  // 0-1 能量阈值
	Device     int     `json:"device"`     // -1 默认设备
	SampleRate int     `json:"sampleRate"` //
}

// CaptureConfig 描述截图行为。
type CaptureConfig struct {
	Mode           string `json:"mode"` // auto | window | fullscreen
	IntervalMS     int    `json:"intervalMs"`
	AutoOnSlide    bool   `json:"autoOnSlide"`
	Hotkey         string `json:"hotkey"`
	CropBlackBars  bool   `json:"cropBlackBars"`
	ExportHighRes  bool   `json:"exportHighRes"`
	KeepSourceFile bool   `json:"keepSourceFile"`
	MaxWidth       int    `json:"maxWidth"`
	JPEGQuality    int    `json:"jpegQuality"`
	DiffThreshold  int    `json:"diffThreshold"` // 0-64，越大越迟钝
}

// LessonConfig 描述课堂监听策略。
type LessonConfig struct {
	Subjects        []string         `json:"subjects"` // 启用监听的学科
	AutoStart       bool             `json:"autoStart"`
	AutoStop        bool             `json:"autoStop"`
	AutoReport      bool             `json:"autoReport"`
	AnalyzeSlides   bool             `json:"analyzeSlides"`
	NotesLanguage   string           `json:"notesLanguage"`
	ManualTimetable []TimetableEntry `json:"manualTimetable,omitempty"`
}

// TimetableEntry 是手动课表中的一节课。
type TimetableEntry struct {
	Weekday int    `json:"weekday"` // 0=周日 ... 6=周六
	Start   string `json:"start"`   // HH:MM
	End     string `json:"end"`     //
	Subject string `json:"subject"` //
	Room    string `json:"room"`    //
}

// IntegrationsConfig 汇总外部软件联动配置。
type IntegrationsConfig struct {
	ClassIsland ClassIslandConfig `json:"classIsland"`
	ICC         ICCConfig         `json:"icc"`
}

// ClassIslandConfig 描述课表软件联动。
type ClassIslandConfig struct {
	Enabled bool   `json:"enabled"`
	DataDir string `json:"dataDir"`
	ExePath string `json:"exePath"`
	PollMS  int    `json:"pollMs"`
}

// ICCConfig 描述 InkCanvasForClass CE 联动。
type ICCConfig struct {
	Enabled    bool   `json:"enabled"`
	ExePath    string `json:"exePath"`
	PluginDir  string `json:"pluginDir"`
	AutoInject bool   `json:"autoInject"`
	PluginSrc  string `json:"pluginSrc"`
}

// UIConfig 描述界面偏好。
type CustomTheme struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Note string            `json:"note,omitempty"`
	Vars map[string]string `json:"vars"`
}

type UIConfig struct {
	Theme            string        `json:"theme"` // claude | graphite | mist | forest 或自定义 ID
	Accent           string        `json:"accent"`
	Density          string        `json:"density"` // comfortable | compact
	SidebarCollapsed bool          `json:"sidebarCollapsed"`
	FontScale        int           `json:"fontScale"`
	CustomThemes     []CustomTheme `json:"customThemes,omitempty"`
	ShowFloatBall    bool          `json:"showFloatBall"`
}

// AgentConfig 控制本地 Agent 与技能包。
type AgentConfig struct {
	// Permission: all 全部访问 | remind 敏感操作提醒 | review 全部人工审查
	Permission string `json:"permission"`
	// Model 课堂助手使用的模型（空 = 跟随 provider）
	Model string `json:"model"`
	// SkillsDir 技能包目录（空 = 数据目录下的 plugins/skills）
	SkillsDir string `json:"skillsDir"`
}

// Store 提供线程安全的配置访问与落盘。
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// Default 返回默认配置。
func Default() Config {
	return Config{
		Version: 1,
		Server: ServerConfig{
			Access: "local",
			Token:  "",
		},
		Provider: ProviderConfig{
			Mode:        "api",
			Preset:      "deepseek",
			BaseURL:     "https://api.deepseek.com/v1",
			Model:       "deepseek-chat",
			Protocol:    "openai",
			Temperature: 0.3,
			MaxTokens:   4096,
			Vision:      false,
			TimeoutSec:  120,
		},
		WebAI: WebAIConfig{
			Site:         "qwen",
			URL:          "https://chat.qwen.ai/",
			Headless:     false,
			ReplyTimeout: 180,
			Selectors:    map[string]string{},
			ChatMode:     "per-subject",
		},
		ASR: ASRConfig{
			Mode:       "api",
			BaseURL:    "https://api.siliconflow.cn/v1",
			Model:      "FunAudioLLM/SenseVoiceSmall",
			Language:   "zh",
			ChunkSec:   10,
			SilenceMS:  700,
			Threshold:  0.012,
			Device:     -1,
			SampleRate: 16000,
		},
		Capture: CaptureConfig{
			Mode:          "auto",
			IntervalMS:    700,
			AutoOnSlide:   true,
			Hotkey:        "Ctrl+Alt+S",
			CropBlackBars: true,
			ExportHighRes: true,
			MaxWidth:      1600,
			JPEGQuality:   88,
			DiffThreshold: 6,
		},
		Lesson: LessonConfig{
			Subjects:      []string{"语文"},
			AutoStart:     true,
			AutoStop:      true,
			AutoReport:    true,
			AnalyzeSlides: true,
			NotesLanguage: "zh",
		},
		Integ: IntegrationsConfig{
			ClassIsland: ClassIslandConfig{Enabled: true, PollMS: 5000},
			ICC:         ICCConfig{Enabled: true, AutoInject: true},
		},
		UI: UIConfig{
			Theme:     "claude",
			Accent:    "#C96442",
			Density:   "comfortable",
			FontScale: 100,
		},
	}
}

// Load 读取配置；文件不存在时写入默认值。
// NewToken 生成访问密钥：cn_ + 24 位十六进制随机串。
func NewToken() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极端情况下的退化随机
		return fmt.Sprintf("cn_%012x", time.Now().UnixNano())
	}
	return "cn_" + hex.EncodeToString(b[:])
}

func Load(path string) (*Store, error) {
	s := &Store{path: path, cfg: Default()}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if e := s.saveLocked(); e != nil {
				return s, e
			}
			return s, nil
		}
		return s, err
	}
	// 以默认值为底，避免旧版本配置缺字段。
	cfg := Default()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return s, err
	}
	cfg.normalize()
	s.cfg = cfg
	return s, nil
}

func (c *Config) normalize() {
	d := Default()
	if c.Server.Access == "" {
		c.Server.Access = "local"
	}
	if c.Server.Access != "lan" {
		c.Server.Access = "local"
	}
	if len(c.Server.Token) < 12 {
		c.Server.Token = NewToken()
	}
	if c.Capture.IntervalMS < 200 {
		c.Capture.IntervalMS = d.Capture.IntervalMS
	}
	if c.Capture.MaxWidth < 480 {
		c.Capture.MaxWidth = d.Capture.MaxWidth
	}
	if c.Capture.JPEGQuality < 40 {
		c.Capture.JPEGQuality = d.Capture.JPEGQuality
	}
	if c.Agent.Permission == "" {
		c.Agent.Permission = "remind"
	}
	if c.Agent.Permission != "all" && c.Agent.Permission != "remind" && c.Agent.Permission != "review" {
		c.Agent.Permission = "remind"
	}
	if c.GitHub.Repo == "" {
		c.GitHub.Repo = "tangyuan9325/ClassNote"
	}
	if c.ASR.Mode == "" {
		c.ASR.Mode = "local"
	}
	if c.ASR.LocalPort == 0 {
		c.ASR.LocalPort = 18095
	}
	if c.ASR.SampleRate == 0 {
		c.ASR.SampleRate = d.ASR.SampleRate
	}
	if c.ASR.ChunkSec <= 0 {
		c.ASR.ChunkSec = d.ASR.ChunkSec
	}
	if c.ASR.SilenceMS <= 0 {
		c.ASR.SilenceMS = d.ASR.SilenceMS
	}
	if c.ASR.Threshold <= 0 {
		c.ASR.Threshold = d.ASR.Threshold
	}
	if c.Provider.TimeoutSec <= 0 {
		c.Provider.TimeoutSec = d.Provider.TimeoutSec
	}
	if c.Provider.MaxTokens <= 0 {
		c.Provider.MaxTokens = d.Provider.MaxTokens
	}
	if c.WebAI.ReplyTimeout <= 0 {
		c.WebAI.ReplyTimeout = d.WebAI.ReplyTimeout
	}
	if c.Integ.ClassIsland.PollMS <= 0 {
		c.Integ.ClassIsland.PollMS = d.Integ.ClassIsland.PollMS
	}
	if c.UI.Theme == "" {
		c.UI.Theme = d.UI.Theme
	}
	if !c.UI.ShowFloatBall && c.UI.Theme != "" {
		// 默认显示悬浮球（用户可在外观设置里关掉）
		c.UI.ShowFloatBall = true
	}
	if c.UI.FontScale <= 0 {
		c.UI.FontScale = 100
	}
	if len(c.Lesson.Subjects) == 0 {
		c.Lesson.Subjects = d.Lesson.Subjects
	}
	if c.Version == 0 {
		c.Version = d.Version
	}
}

// Get 返回配置副本。
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Update 在写锁内修改配置并落盘。
func (s *Store) Update(fn func(*Config) error) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.cfg
	if err := fn(&cp); err != nil {
		return s.cfg, err
	}
	cp.normalize()
	s.cfg = cp
	return cp, s.saveLocked()
}

// Save 落盘当前配置。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	s.cfg.LastRun = time.Now()
	raw, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Path 返回配置文件路径。
func (s *Store) Path() string { return s.path }

// Redacted 返回用于前端展示的配置副本（隐藏密钥明文）。
func (s *Store) Redacted() map[string]any {
	c := s.Get()
	key := c.Provider.APIKey
	if key != "" {
		key = maskKey(key)
	}
	asrKey := c.ASR.APIKey
	if asrKey != "" {
		asrKey = maskKey(asrKey)
	}
	c.Provider.APIKey = key
	c.ASR.APIKey = asrKey
	// GitHub Token 同样要脱敏：它会随 /api/bootstrap 下发到前端，
	// 而局域网模式下任何持访问密钥的设备都能读到。
	ghToken := c.GitHub.Token
	if ghToken != "" {
		ghToken = maskKey(ghToken)
	}
	c.GitHub.Token = ghToken
	return map[string]any{
		"config":     c,
		"hasKey":     s.Get().Provider.APIKey != "",
		"hasASRKey":  s.Get().ASR.APIKey != "",
		"hasGHToken": s.Get().GitHub.Token != "",
	}
}

func maskKey(k string) string {
	if len(k) <= 8 {
		return "••••••"
	}
	return k[:4] + "••••••" + k[len(k)-4:]
}
