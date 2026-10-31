package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"classnote/internal/config"
)

// OpenAIClient 兼容 OpenAI Chat Completions 协议的服务。
type OpenAIClient struct {
	cfg config.ProviderConfig
}

// Describe 返回后端描述。
func (c *OpenAIClient) Describe() string {
	return fmt.Sprintf("API · %s · %s", hostOf(c.cfg.BaseURL), c.cfg.Model)
}

// Ready 表示配置是否完整。
func (c *OpenAIClient) Ready() bool {
	return c.cfg.BaseURL != "" && c.cfg.Model != ""
}

type oaContent struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaImageURL struct {
	URL string `json:"url"`
}

type oaMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type oaRequest struct {
	Model       string      `json:"model"`
	Messages    []oaMessage `json:"messages"`
	Temperature float64     `json:"temperature,omitempty"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
	Stream      bool        `json:"stream"`
	StreamOpts  *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (c *OpenAIClient) buildBody(msgs []Message, opt Options, stream bool) (*oaRequest, error) {
	out := make([]oaMessage, 0, len(msgs))
	for _, m := range msgs {
		if len(m.Parts) == 0 {
			continue
		}
		onlyText := true
		for _, p := range m.Parts {
			if p.Type != "text" {
				onlyText = false
			}
		}
		if onlyText {
			out = append(out, oaMessage{Role: m.Role, Content: m.Text()})
			continue
		}
		parts := make([]oaContent, 0, len(m.Parts))
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				if p.Text != "" {
					parts = append(parts, oaContent{Type: "text", Text: p.Text})
				}
			case "image":
				mime := p.MIME
				if mime == "" {
					mime = "image/png"
				}
				parts = append(parts, oaContent{Type: "image_url", ImageURL: &oaImageURL{
					URL: "data:" + mime + ";base64," + p.ImageB64,
				}})
			}
		}
		out = append(out, oaMessage{Role: m.Role, Content: parts})
	}
	body := &oaRequest{
		Model:       c.cfg.Model,
		Messages:    out,
		Temperature: opt.Temperature,
		MaxTokens:   opt.MaxTokens,
		Stream:      stream,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = c.cfg.MaxTokens
	}
	if opt.JSON && stream == false {
		body.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}
	}
	if stream {
		body.StreamOpts = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true}
	}
	return body, nil
}

func (c *OpenAIClient) url() string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

func (c *OpenAIClient) timeout(opt Options) time.Duration {
	sec := opt.TimeoutSec
	if sec <= 0 {
		sec = c.cfg.TimeoutSec
	}
	if sec <= 0 {
		sec = 120
	}
	return time.Duration(sec) * time.Second
}

func (c *OpenAIClient) do(ctx context.Context, body *oaRequest, opt Options) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout(opt))
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.url(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

// Chat 同步调用。
func (c *OpenAIClient) Chat(ctx context.Context, msgs []Message, opt Options) (*Result, error) {
	body, err := c.buildBody(msgs, opt, false)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, body, opt)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("模型接口返回 %d: %s", resp.StatusCode, summarizeErr(raw))
	}
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析模型响应失败: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("模型错误: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("模型未返回内容")
	}
	return &Result{
		Text:  out.Choices[0].Message.Content,
		Model: firstNonEmpty(out.Model, c.cfg.Model),
		Via:   "openai",
		Usage: Usage{out.Usage.PromptTokens, out.Usage.CompletionTokens, out.Usage.TotalTokens},
	}, nil
}

// Stream 流式调用。
func (c *OpenAIClient) Stream(ctx context.Context, msgs []Message, opt Options, onDelta func(string)) (*Result, error) {
	body, err := c.buildBody(msgs, opt, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, body, opt)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("模型接口返回 %d: %s", resp.StatusCode, summarizeErr(raw))
	}
	var sb strings.Builder
	var usage Usage
	model := c.cfg.Model

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk oaResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("模型错误: %s", chunk.Error.Message)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage.TotalTokens > 0 {
			usage = Usage{chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, chunk.Usage.TotalTokens}
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				sb.WriteString(ch.Delta.Content)
				if onDelta != nil {
					onDelta(ch.Delta.Content)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取流式响应失败: %w", err)
	}
	return &Result{Text: sb.String(), Model: model, Via: "openai", Usage: usage}, nil
}

func summarizeErr(raw []byte) string {
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err == nil && out.Error != nil {
		return out.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len([]rune(s)) > 300 {
		s = string([]rune(s)[:300]) + "…"
	}
	return s
}

func hostOf(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if i := strings.IndexAny(u, "/:"); i > 0 {
		return u[:i]
	}
	return u
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
