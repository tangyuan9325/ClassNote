// Package llm 提供统一的大模型客户端：OpenAI 兼容协议、Anthropic 协议与网页版自动化。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"classnote/internal/config"
)

// Role 消息角色。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Part 是消息内容的一部分，可以是文本或图片。
type Part struct {
	Type     string `json:"type"` // text | image
	Text     string `json:"text,omitempty"`
	ImageB64 string `json:"imageB64,omitempty"`
	MIME     string `json:"mime,omitempty"`
}

// Message 是一条对话消息。
type Message struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

// TextMessage 构造纯文本消息。
func TextMessage(role, text string) Message {
	return Message{Role: role, Parts: []Part{{Type: "text", Text: text}}}
}

// UserText 构造用户文本消息。
func UserText(text string) Message { return TextMessage(RoleUser, text) }

// UserVision 构造带图片的用户消息。
func UserVision(text, imageB64, mime string) Message {
	return Message{Role: RoleUser, Parts: []Part{
		{Type: "text", Text: text},
		{Type: "image", ImageB64: imageB64, MIME: mime},
	}}
}

// Text 拼接消息中的全部文本。
func (m Message) Text() string {
	var sb strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// Options 单次请求参数。
type Options struct {
	Temperature float64
	MaxTokens   int
	JSON        bool
	TimeoutSec  int
	// Subject 当前学科：网页版用它区分对话历史，同学科共用一条、
	// 换学科自动开新对话，避免历史记录里堆满垃圾。
	Subject string
	// FollowUp 同一条对话里的后续提问：网页版可以精简提示词，
	// 不再重复工具与背景说明（对话上下文里已经有了）。
	FollowUp bool
}

// Usage 统计 token 消耗。
type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// Result 是一次调用结果。
type Result struct {
	Text  string `json:"text"`
	Usage Usage  `json:"usage"`
	Model string `json:"model"`
	Via   string `json:"via"`
}

// Client 抽象不同后端。
type Client interface {
	// Chat 同步返回完整回复。
	Chat(ctx context.Context, msgs []Message, opt Options) (*Result, error)
	// Stream 流式返回增量文本，onDelta 可为 nil。
	Stream(ctx context.Context, msgs []Message, opt Options, onDelta func(string)) (*Result, error)
	// Describe 返回用于界面展示的后端描述。
	Describe() string
	// Ready 表示后端是否可用。
	Ready() bool
}

// ChatManager 由网页版客户端实现，供设置页管理「每科一条对话」。
// API 模型没有对话概念，不做实现。
type ChatManager interface {
	Chats() []WebChat
	SetChat(subject, url string) error
	ClearChat(subject string) bool
	Prewarm(ctx context.Context, subjects []string, system string, force bool) []PrewarmResult
	ChatStatus() ChatStatus
	SetPriming(busy bool, last []PrewarmResult)
	LastPrewarm() []PrewarmResult
}

// ChatManagerOf 从客户端里取出对话管理能力，没有则返回 nil。
func ChatManagerOf(c Client) ChatManager {
	if m, ok := c.(ChatManager); ok {
		return m
	}
	return nil
}

// ErrNotConfigured 表示缺少配置。
var ErrNotConfigured = errors.New("模型尚未完成配置")

// New 根据配置创建客户端。
func New(cfg config.Config) (Client, error) {
	if cfg.Provider.Mode == "web" {
		return NewWebClient(cfg)
	}
	switch cfg.Provider.Protocol {
	case "", "openai":
		if cfg.Provider.APIKey == "" && !strings.Contains(cfg.Provider.BaseURL, "localhost") && !strings.Contains(cfg.Provider.BaseURL, "127.0.0.1") {
			return nil, fmt.Errorf("%w: 缺少 API Key", ErrNotConfigured)
		}
		if cfg.Provider.BaseURL == "" || cfg.Provider.Model == "" {
			return nil, fmt.Errorf("%w: 缺少 Base URL 或模型名", ErrNotConfigured)
		}
		return &OpenAIClient{cfg: cfg.Provider}, nil
	case "anthropic":
		if cfg.Provider.APIKey == "" {
			return nil, fmt.Errorf("%w: 缺少 API Key", ErrNotConfigured)
		}
		return &AnthropicClient{cfg: cfg.Provider}, nil
	default:
		return nil, fmt.Errorf("不支持的协议: %s", cfg.Provider.Protocol)
	}
}

// Preset 是界面上的服务商预设。
type Preset struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Protocol string   `json:"protocol"`
	BaseURL  string   `json:"baseURL"`
	Models   []string `json:"models"`
	Vision   bool     `json:"vision"`
	Note     string   `json:"note"`
	KeyURL   string   `json:"keyURL"`
}

// Presets 返回内置服务商列表。
func Presets() []Preset {
	return []Preset{
		{ID: "deepseek", Name: "DeepSeek", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
			Models: []string{"deepseek-chat", "deepseek-reasoner"}, Note: "性价比高，中文课堂场景表现稳定", KeyURL: "https://platform.deepseek.com/api_keys"},
		{ID: "qwen", Name: "通义千问", Protocol: "openai", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
			Models: []string{"qwen-max", "qwen-plus", "qwen-vl-max", "qwen3-vl-plus"}, Vision: true, Note: "原生支持图片理解，适合分析 PPT 画面", KeyURL: "https://bailian.console.aliyun.com/"},
		{ID: "kimi", Name: "Kimi / 月之暗面", Protocol: "openai", BaseURL: "https://api.moonshot.cn/v1",
			Models: []string{"kimi-k2-0905-preview", "moonshot-v1-128k", "moonshot-v1-32k-vision-preview"}, Vision: true, Note: "长上下文，适合整堂课的材料", KeyURL: "https://platform.moonshot.cn/console/api-keys"},
		{ID: "zhipu", Name: "智谱 GLM", Protocol: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4",
			Models: []string{"glm-4.6", "glm-4v-plus"}, Vision: true, Note: "GLM-4V 系列支持图像", KeyURL: "https://open.bigmodel.cn/usercenter/apikeys"},
		{ID: "siliconflow", Name: "SiliconFlow 硅基流动", Protocol: "openai", BaseURL: "https://api.siliconflow.cn/v1",
			Models: []string{"deepseek-ai/DeepSeek-V3", "Qwen/Qwen2.5-VL-72B-Instruct"}, Vision: true, Note: "聚合平台，同时提供语音转写", KeyURL: "https://cloud.siliconflow.cn/account/ak"},
		{ID: "openai", Name: "OpenAI", Protocol: "openai", BaseURL: "https://api.openai.com/v1",
			Models: []string{"gpt-4o-mini", "gpt-4o", "gpt-4.1"}, Vision: true, Note: "需要可访问的网络环境", KeyURL: "https://platform.openai.com/api-keys"},
		{ID: "ollama", Name: "本地 Ollama", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1",
			Models: []string{"qwen2.5:7b", "qwen2.5vl:7b", "llama3.1:8b"}, Vision: true, Note: "完全离线，需要本机已安装 Ollama", KeyURL: "https://ollama.com/download"},
		{ID: "custom", Name: "自定义 / 中转站", Protocol: "openai", BaseURL: "",
			Models: []string{}, Note: "任何 OpenAI 兼容接口", KeyURL: ""},
	}
}

// ExtractJSON 从模型回复中提取 JSON 对象，容忍代码块与前后缀文本。
func ExtractJSON(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		rest = strings.TrimPrefix(rest, "JSON")
		if j := strings.Index(rest, "```"); j >= 0 {
			cand := strings.TrimSpace(rest[:j])
			if strings.HasPrefix(cand, "{") && strings.HasSuffix(cand, "}") {
				return cand, true
			}
		}
	}
	start := strings.Index(s, "{")
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// repairJSON 转义 JSON 字符串值里的裸控制字符。
// 网页版模型常把公式的换行/制表符直接塞进字符串，导致严格解析器报错。
func repairJSON(raw string) string {
	var b strings.Builder
	b.Grow(len(raw) + 16)
	inStr, esc := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inStr {
			if esc {
				b.WriteByte(c)
				esc = false
				continue
			}
			switch c {
			case '\\':
				b.WriteByte(c)
				esc = true
			case '"':
				b.WriteByte(c)
				inStr = false
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					fmt.Fprintf(&b, `\u%04x`, c)
				} else {
					b.WriteByte(c)
				}
			}
			continue
		}
		if c == '"' {
			inStr = true
		}
		b.WriteByte(c)
	}
	return b.String()
}

// DecodeJSON 提取并解析 JSON，自动修裸控制字符。
func DecodeJSON(s string, dst any) error {
	raw, ok := ExtractJSON(s)
	if !ok {
		return fmt.Errorf("回复中找不到 JSON: %s", truncate(s, 200))
	}
	if err := json.Unmarshal([]byte(raw), dst); err == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(repairJSON(raw)), dst); err != nil {
		return fmt.Errorf("解析 JSON 失败: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
