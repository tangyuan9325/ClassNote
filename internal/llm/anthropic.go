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

// AnthropicClient 使用 Anthropic Messages API。
type AnthropicClient struct {
	cfg config.ProviderConfig
}

// Describe 返回后端描述。
func (c *AnthropicClient) Describe() string {
	return fmt.Sprintf("Anthropic · %s", c.cfg.Model)
}

// Ready 表示配置是否完整。
func (c *AnthropicClient) Ready() bool { return c.cfg.APIKey != "" && c.cfg.Model != "" }

type anBlock struct {
	Type   string      `json:"type"`
	Text   string      `json:"text,omitempty"`
	Source *anImageSrc `json:"source,omitempty"`
}

type anImageSrc struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anMessage struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

type anRequest struct {
	Model       string      `json:"model"`
	System      string      `json:"system,omitempty"`
	Messages    []anMessage `json:"messages"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature float64     `json:"temperature,omitempty"`
	Stream      bool        `json:"stream"`
}

type anResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *AnthropicClient) endpoint() string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

func (c *AnthropicClient) build(msgs []Message, opt Options, stream bool) *anRequest {
	var system []string
	out := make([]anMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == RoleSystem {
			system = append(system, m.Text())
			continue
		}
		blocks := make([]anBlock, 0, len(m.Parts))
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				if p.Text != "" {
					blocks = append(blocks, anBlock{Type: "text", Text: p.Text})
				}
			case "image":
				mime := p.MIME
				if mime == "" {
					mime = "image/png"
				}
				blocks = append(blocks, anBlock{Type: "image", Source: &anImageSrc{Type: "base64", MediaType: mime, Data: p.ImageB64}})
			}
		}
		if len(blocks) > 0 {
			out = append(out, anMessage{Role: m.Role, Content: blocks})
		}
	}
	maxTok := c.cfg.MaxTokens
	if opt.MaxTokens > 0 {
		maxTok = opt.MaxTokens
	}
	return &anRequest{
		Model:       c.cfg.Model,
		System:      strings.Join(system, "\n\n"),
		Messages:    out,
		MaxTokens:   maxTok,
		Temperature: opt.Temperature,
		Stream:      stream,
	}
}

func (c *AnthropicClient) do(ctx context.Context, body *anRequest, opt Options) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	sec := opt.TimeoutSec
	if sec <= 0 {
		sec = c.cfg.TimeoutSec
	}
	if sec <= 0 {
		sec = 120
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(sec)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

// Chat 同步调用。
func (c *AnthropicClient) Chat(ctx context.Context, msgs []Message, opt Options) (*Result, error) {
	resp, err := c.do(ctx, c.build(msgs, opt, false), opt)
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
	var out anResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("模型错误: %s", out.Error.Message)
	}
	var sb strings.Builder
	for _, b := range out.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return &Result{
		Text:  sb.String(),
		Model: firstNonEmpty(out.Model, c.cfg.Model),
		Via:   "anthropic",
		Usage: Usage{out.Usage.InputTokens, out.Usage.OutputTokens, out.Usage.InputTokens + out.Usage.OutputTokens},
	}, nil
}

// Stream 流式调用。
func (c *AnthropicClient) Stream(ctx context.Context, msgs []Message, opt Options, onDelta func(string)) (*Result, error) {
	resp, err := c.do(ctx, c.build(msgs, opt, true), opt)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("模型接口返回 %d: %s", resp.StatusCode, summarizeErr(raw))
	}
	var sb strings.Builder
	usage := Usage{}
	model := c.cfg.Model
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message.Model != "" {
				model = ev.Message.Model
			}
			usage.PromptTokens = ev.Message.Usage.InputTokens
		case "content_block_delta":
			if ev.Delta.Text != "" {
				sb.WriteString(ev.Delta.Text)
				if onDelta != nil {
					onDelta(ev.Delta.Text)
				}
			}
		case "message_delta":
			usage.CompletionTokens = ev.Usage.OutputTokens
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return &Result{Text: sb.String(), Model: model, Via: "anthropic", Usage: usage}, nil
}
