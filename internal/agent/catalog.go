package agent

import (
	"fmt"
	"strings"

	"classnote/internal/config"
	"classnote/internal/llm"
)

// 内置的「网页版」模型 ID。它们不需要 API Key，走浏览器自动化。
const (
	ModelFollow      = ""
	ModelWebQwen     = "web:qwen"
	ModelWebDeepSeek = "web:deepseek"
)

// ModelOption 是模型下拉框里的一项。
type ModelOption struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"` // follow | web | api
	Note  string `json:"note,omitempty"`
	Ready bool   `json:"ready"`
}

// Catalog 返回 Agent 可选的模型列表。
//
// 前三项是固定的：跟随全局设置、网页版 Qwen、网页版 DeepSeek。
// 后两项来自当前 API 配置（有 Key 才显示为可用）。
// 「网页版」排在 API 之前，是因为大多数老师没有 API Key，但有浏览器。
func Catalog(cfg config.Config) []ModelOption {
	out := []ModelOption{{
		ID: ModelFollow, Name: "跟随全局设置", Kind: "follow",
		Note:  describeProvider(cfg),
		Ready: providerReady(cfg),
	}}
	out = append(out,
		ModelOption{
			ID: ModelWebQwen, Name: "网页版 Qwen（免密钥）", Kind: "web",
			Note:  "用浏览器打开 chat.qwen.ai，复用登录态；课件图片与文本都会走浏览器",
			Ready: true,
		},
		ModelOption{
			ID: ModelWebDeepSeek, Name: "网页版 DeepSeek（免密钥）", Kind: "web",
			Note:  "用浏览器打开 chat.deepseek.com，复用登录态",
			Ready: true,
		},
	)
	if cfg.Provider.Mode != "web" && cfg.Provider.Model != "" {
		out = append(out, ModelOption{
			ID: "api:" + cfg.Provider.Model, Name: "API · " + cfg.Provider.Model, Kind: "api",
			Note: cfg.Provider.BaseURL, Ready: cfg.Provider.APIKey != "" || isLocal(cfg.Provider.BaseURL),
		})
	}
	return out
}

// ClientFor 按模型 ID 造一个客户端。id 为空表示跟随全局配置。
func ClientFor(cfg config.Config, id string) (llm.Client, error) {
	switch {
	case id == "":
		return llm.New(cfg)
	case strings.HasPrefix(id, "web:"):
		site := strings.TrimPrefix(id, "web:")
		c := cfg
		c.Provider.Mode = "web"
		c.WebAI.Site = site
		switch site {
		case "qwen":
			if c.WebAI.URL == "" || strings.Contains(c.WebAI.URL, "deepseek") {
				c.WebAI.URL = "https://chat.qwen.ai/"
			}
		case "deepseek":
			c.WebAI.URL = "https://chat.deepseek.com/"
		default:
			return nil, fmt.Errorf("不认识的网页版站点 %q", site)
		}
		return llm.New(c)
	case strings.HasPrefix(id, "api:"):
		model := strings.TrimPrefix(id, "api:")
		c := cfg
		c.Provider.Mode = "api"
		c.Provider.Model = model
		return llm.New(c)
	default:
		return nil, fmt.Errorf("不认识的模型 %q", id)
	}
}

// DescribeModel 返回给界面看的模型名。
func DescribeModel(id string) string {
	switch id {
	case ModelFollow:
		return "跟随全局设置"
	case ModelWebQwen:
		return "网页版 Qwen"
	case ModelWebDeepSeek:
		return "网页版 DeepSeek"
	}
	return strings.TrimPrefix(id, "api:")
}

func describeProvider(cfg config.Config) string {
	if cfg.Provider.Mode == "web" {
		return "当前是网页版：" + cfg.WebAI.Site
	}
	if cfg.Provider.Model == "" {
		return "尚未配置模型"
	}
	return cfg.Provider.Model
}

func providerReady(cfg config.Config) bool {
	if cfg.Provider.Mode == "web" {
		return cfg.WebAI.LoggedIn
	}
	if cfg.Provider.APIKey != "" {
		return true
	}
	return isLocal(cfg.Provider.BaseURL)
}

func isLocal(u string) bool {
	return strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost")
}
