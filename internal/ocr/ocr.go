// Package ocr 调用文档识别服务，把一页画面转成文字。
//
// 默认面向 baidu/Unlimited-OCR：它是一个 3B 的文档解析模型，跑在本地 GPU 上，
// 通过 vLLM / SGLang 暴露 OpenAI 兼容接口，一张图一次前向就能输出整页 Markdown，
// 对手写、表格、公式、多栏排版的还原明显好过传统逐块 OCR。
//
// 这里刻意只依赖 OpenAI 兼容协议：换 PaddleOCR-VL、GOT-OCR、dots.ocr 或任何
// 支持 image_url 的多模态服务都能直接接线，用户不必为了换模型改程序。
package ocr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 默认参数：与官方 docker / SGLang 样例的端口保持一致。
const (
	DefaultBaseURL  = "http://127.0.0.1:10000/v1"
	DefaultModel    = "Unlimited-OCR"
	DefaultPrompt   = "document parsing."
	DefaultTimeout  = 90
	DefaultMaxChars = 6000

	// 单张图片上限。1600px 的 JPEG 通常只有 200KB 左右，12MB 足够覆盖
	// 4K 截图；再大就不是「一页幻灯片」而是整块屏幕录像了。
	maxImageBytes = 12 << 20
)

// Settings 是 OCR 服务的连接参数。
type Settings struct {
	Enabled    bool   `json:"enabled"`
	BaseURL    string `json:"baseUrl"`
	APIKey     string `json:"apiKey"`
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`
	TimeoutSec int    `json:"timeoutSec"`
	MaxChars   int    `json:"maxChars"`
}

// Normalize 补齐空字段。老配置里没有 OCR 段时也能直接调用。
func (s Settings) Normalize() Settings {
	if strings.TrimSpace(s.BaseURL) == "" {
		s.BaseURL = DefaultBaseURL
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if strings.TrimSpace(s.Model) == "" {
		s.Model = DefaultModel
	}
	if strings.TrimSpace(s.Prompt) == "" {
		s.Prompt = DefaultPrompt
	}
	if s.TimeoutSec <= 0 {
		s.TimeoutSec = DefaultTimeout
	}
	if s.TimeoutSec > 600 {
		s.TimeoutSec = 600
	}
	if s.MaxChars <= 0 {
		s.MaxChars = DefaultMaxChars
	}
	if s.MaxChars < 200 {
		s.MaxChars = 200
	}
	return s
}

// Result 是一次识别的结果。
type Result struct {
	Text      string `json:"text"`
	Model     string `json:"model"`
	LatencyMS int    `json:"latencyMs"`
	Chars     int    `json:"chars"`
	Truncated bool   `json:"truncated"`
}

// Client 是 OCR 服务客户端，零值不可用，必须用 New 构造。
type Client struct {
	s  Settings
	hc *http.Client
}

// New 按配置创建客户端。
func New(s Settings) *Client {
	s = s.Normalize()
	return &Client{
		s: s,
		hc: &http.Client{
			// 单页识别在消费级显卡上通常 2-8 秒；超时给足，但必须有上限，
			// 否则服务卡死会让分析协程一直挂着。
			Timeout: time.Duration(s.TimeoutSec) * time.Second,
		},
	}
}

// Enabled 表示服务已在配置中开启。
func (c *Client) Enabled() bool { return c != nil && c.s.Enabled }

// Settings 返回生效参数（已补齐默认值）。
func (c *Client) Settings() Settings {
	if c == nil {
		return Settings{}.Normalize()
	}
	return c.s
}

// Describe 给日志和界面用的一句话描述，不含密钥。
func (c *Client) Describe() string {
	if c == nil {
		return ""
	}
	host := c.s.BaseURL
	if u, err := url.Parse(c.s.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return fmt.Sprintf("%s @ %s", c.s.Model, host)
}

// Recognize 识别一张图片（JPEG/PNG 字节）。
func (c *Client) Recognize(ctx context.Context, img []byte, mime string) (Result, error) {
	if c == nil {
		return Result{}, errors.New("OCR 未配置")
	}
	if len(img) == 0 {
		return Result{}, errors.New("图片为空，无法识别")
	}
	if len(img) > maxImageBytes {
		return Result{}, fmt.Errorf("图片过大（%d MB），请先压缩", len(img)/(1<<20))
	}
	if mime == "" {
		mime = "image/jpeg"
	}

	body := map[string]any{
		"model": c.s.Model,
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				// 官方样例把文本放在图片之前；Unlimited-OCR 的 chat 模板按这个顺序取 prompt。
				map[string]any{"type": "text", "text": c.s.Prompt},
				map[string]any{"type": "image_url", "image_url": map[string]any{
					"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img),
				}},
			},
		}},
		"temperature": 0,
		"max_tokens":  4096,
		"stream":      false,
	}
	raw, _ := json.Marshal(body)

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.s.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.s.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.s.APIKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return Result{}, friendlyError(err, c.s.BaseURL)
	}
	defer resp.Body.Close()
	// 识别结果可能有几十 KB（整页 Markdown），给 4MB 上限。
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode != http.StatusOK {
		return Result{}, statusError(resp.StatusCode, payload, c.s.BaseURL)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return Result{}, fmt.Errorf("OCR 服务返回的不是合法 JSON：%s", clip(string(payload), 120))
	}
	if out.Error != nil && out.Error.Message != "" {
		return Result{}, fmt.Errorf("OCR 服务报错：%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return Result{}, errors.New("OCR 服务没有返回结果")
	}

	text := contentText(out.Choices[0].Message.Content)
	text, truncated := Clean(text, c.s.MaxChars)
	if strings.TrimSpace(text) == "" {
		return Result{Model: c.s.Model, LatencyMS: int(time.Since(start).Milliseconds())}, nil
	}
	return Result{
		Text:      text,
		Model:     c.s.Model,
		LatencyMS: int(time.Since(start).Milliseconds()),
		Chars:     len([]rune(text)),
		Truncated: truncated,
	}, nil
}

// contentText 兼容两种返回形态：字符串，或 [{type:"text",text:"..."}]。
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "" || p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// Clean 清理 OCR 输出，并按上限截断。
//
// 模型有时会回显提示词、套一层 Markdown 代码块，或者在坐标模式下把
// `<img>` 与 `\n` 前缀一起吐出来；这些都不该进笔记。
func Clean(s string, maxChars int) (string, bool) {
	t := strings.ReplaceAll(s, "\r\n", "\n")
	t = strings.ReplaceAll(t, "\r", "\n")
	// 去掉坐标模式的特殊前缀（原样是字面量 `\n`，不是换行）。
	t = strings.ReplaceAll(t, "<img>", "")
	t = strings.ReplaceAll(t, `\n`, "\n")

	// 整体被一个代码块包住时脱壳；只脱一层，避免误伤正文里的代码。
	trimmed := strings.TrimSpace(t)
	if strings.HasPrefix(trimmed, "```") && strings.HasSuffix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 3 {
			lines = lines[1 : len(lines)-1]
			trimmed = strings.Join(lines, "\n")
		}
	}
	// 模型把提示词原样回显的情况（整段只有 "document parsing."）。
	if len([]rune(trimmed)) <= 24 && strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(trimmed), "."), "document parsing") {
		return "", false
	}

	lines := strings.Split(trimmed, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if strings.TrimSpace(ln) == "" {
			blank++
			if blank > 1 { // 连续空行压成一行
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, ln)
	}
	t = strings.TrimSpace(strings.Join(out, "\n"))

	if maxChars > 0 {
		r := []rune(t)
		if len(r) > maxChars {
			return string(r[:maxChars]) + "…", true
		}
	}
	return t, false
}

// friendlyError 把网络错误翻译成用户能照做的提示。
func friendlyError(err error, base string) error {
	host := base
	if u, e := url.Parse(base); e == nil && u.Host != "" {
		host = u.Host
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "actively refused"),
		strings.Contains(lower, "no route to host"),
		strings.Contains(lower, "connectex"):
		return fmt.Errorf("OCR 服务没有启动（%s）。先运行 Unlimited-OCR 服务，或在设置里改地址", host)
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"):
		return fmt.Errorf("OCR 识别超时（%s）。服务可能还在加载模型，或这一页太复杂", host)
	case strings.Contains(lower, "no such host"):
		return fmt.Errorf("OCR 服务地址解析失败（%s），请检查拼写", host)
	}
	return fmt.Errorf("连不上 OCR 服务（%s）：%s", host, msg)
}

func statusError(code int, payload []byte, base string) error {
	body := clip(strings.TrimSpace(string(payload)), 160)
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("OCR 服务拒绝访问（%d），请检查 API Key", code)
	case http.StatusNotFound:
		return fmt.Errorf("OCR 接口不存在（404）。地址应以 /v1 结尾，例如 %s", DefaultBaseURL)
	case http.StatusRequestEntityTooLarge:
		return errors.New("图片太大，OCR 服务拒绝接收（413）")
	}
	if body != "" {
		return fmt.Errorf("OCR 服务返回 %d：%s", code, body)
	}
	return fmt.Errorf("OCR 服务返回 %d", code)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
