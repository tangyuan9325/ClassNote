// Package theme 管理自定义界面主题与主题市场。
package theme

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"classnote/internal/config"
)

// Builtin 是内置主题的元信息（配色在前端 CSS 里）。
type Builtin struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Note   string `json:"note"`
	Swatch string `json:"swatch"`
}

// Builtins 返回内置主题列表。
func Builtins() []Builtin {
	return []Builtin{
		{ID: "claude", Name: "羊皮纸", Note: "米白底 / 陶土红点缀", Swatch: "#C96442"},
		{ID: "graphite", Name: "石墨", Note: "深灰底 / 冷白文字", Swatch: "#8A9BA8"},
		{ID: "mist", Name: "雾蓝", Note: "冷调灰蓝 / 低对比", Swatch: "#5B7C99"},
		{ID: "forest", Name: "松林", Note: "墨绿 / 暖木色", Swatch: "#4E6B51"},
	}
}

// Vars 是可自定义的 CSS 变量名（与 web/css/app.css 对齐）。
var Vars = []string{
	"bg", "bg-panel", "bg-side", "bg-sunken",
	"ink", "ink-2", "ink-3",
	"line", "line-strong",
	"accent", "accent-ink", "accent-soft",
	"ok", "warn", "danger",
}

var idRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

// Validate 检查自定义主题。
func Validate(t config.CustomTheme) error {
	if !idRe.MatchString(t.ID) {
		return fmt.Errorf("主题 ID 只能是字母/数字/下划线/连字符（1-32 位）")
	}
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("主题名称不能为空")
	}
	if len(t.Vars) == 0 {
		return fmt.Errorf("主题至少要有一个配色变量")
	}
	for k, v := range t.Vars {
		if !isHexColor(v) && v != "" {
			return fmt.Errorf("变量 %s 的值 %q 不是合法颜色（形如 #1f1e1d）", k, v)
		}
	}
	return nil
}

var hexRe = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)

func isHexColor(s string) bool { return hexRe.MatchString(s) }

// Save 保存/覆盖一个自定义主题。
func Save(cfg *config.Config, t config.CustomTheme) error {
	if err := Validate(t); err != nil {
		return err
	}
	for i := range cfg.UI.CustomThemes {
		if cfg.UI.CustomThemes[i].ID == t.ID {
			cfg.UI.CustomThemes[i] = t
			return nil
		}
	}
	cfg.UI.CustomThemes = append(cfg.UI.CustomThemes, t)
	return nil
}

// Delete 删除自定义主题。
func Delete(cfg *config.Config, id string) bool {
	out := cfg.UI.CustomThemes[:0]
	found := false
	for _, t := range cfg.UI.CustomThemes {
		if t.ID == id {
			found = true
			continue
		}
		out = append(out, t)
	}
	cfg.UI.CustomThemes = out
	return found
}

// Find 查找自定义主题。
func Find(cfg config.Config, id string) (config.CustomTheme, bool) {
	for _, t := range cfg.UI.CustomThemes {
		if t.ID == id {
			return t, true
		}
	}
	return config.CustomTheme{}, false
}

// CSS 把主题变量渲染成 CSS 片段（前端注入用）。
func CSS(t config.CustomTheme) string {
	var b strings.Builder
	b.WriteString(":root{\n")
	for _, k := range Vars {
		if v, ok := t.Vars[k]; ok && v != "" {
			fmt.Fprintf(&b, "  --%s: %s;\n", k, v)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// ExportJSON 导出主题（可分享）。
func ExportJSON(t config.CustomTheme) []byte {
	type wrap struct {
		Format  string             `json:"format"`
		Version int                `json:"version"`
		Theme   config.CustomTheme `json:"theme"`
	}
	raw, _ := json.MarshalIndent(wrap{Format: "classnote-theme", Version: 1, Theme: t}, "", "  ")
	return raw
}

// ImportJSON 导入主题（可来自主题市场或文件）。
func ImportJSON(raw []byte) (config.CustomTheme, error) {
	var w struct {
		Theme config.CustomTheme `json:"theme"`
		// 允许直接传主题本体
		ID   string            `json:"id"`
		Name string            `json:"name"`
		Vars map[string]string `json:"vars"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return config.CustomTheme{}, fmt.Errorf("解析失败: %w", err)
	}
	t := w.Theme
	if t.ID == "" && w.ID != "" {
		t = config.CustomTheme{ID: w.ID, Name: w.Name, Vars: w.Vars}
	}
	if t.ID == "" {
		return config.CustomTheme{}, fmt.Errorf("主题缺少 id 字段")
	}
	if err := Validate(t); err != nil {
		return config.CustomTheme{}, err
	}
	return t, nil
}

var cli = &http.Client{Timeout: 20 * time.Second}

//go:embed themes.json
var bundledThemes []byte

// LocalRegistry 是随包的主题清单（离线兜底）。
var LocalRegistry = bundledThemes

// LoadBundled 确保随包清单已载入（幂等）。
func LoadBundled() {
	if len(LocalRegistry) == 0 {
		LocalRegistry = bundledThemes
	}
}

// localMarket 读取随包清单。
func localMarket() []MarketItem {
	if len(LocalRegistry) == 0 {
		return nil
	}
	var w struct {
		Themes []MarketItem `json:"themes"`
	}
	if err := json.Unmarshal(LocalRegistry, &w); err == nil {
		return w.Themes
	}
	var arr []MarketItem
	_ = json.Unmarshal(LocalRegistry, &arr)
	return arr
}

// MarketItem 是主题市场里的一条。
type MarketItem struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Note   string            `json:"note,omitempty"`
	Author string            `json:"author,omitempty"`
	Swatch string            `json:"swatch,omitempty"`
	Vars   map[string]string `json:"vars,omitempty"`
	URL    string            `json:"url,omitempty"` // 可选：详情地址
}

// Market 从远端清单拉取主题市场条目。
//
// registry 是一个 JSON 地址，返回 {"themes":[...]} 或直接是数组。
// 默认用项目仓库里的 themes.json。
func Market(registry string) ([]MarketItem, error) {
	if registry == "" {
		registry = "https://raw.githubusercontent.com/tangyuan9325/ClassNote/main/themes.json"
	}
	req, err := http.NewRequest("GET", registry, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ClassNote")
	resp, err := cli.Do(req)
	if err != nil {
		// 离线时回退到随包的 themes.json
		if local := localMarket(); len(local) > 0 {
			return local, nil
		}
		return nil, fmt.Errorf("连接主题市场失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("主题市场返回 %d", resp.StatusCode)
	}
	var w struct {
		Themes []MarketItem `json:"themes"`
	}
	if err := json.Unmarshal(raw, &w); err == nil && len(w.Themes) > 0 {
		return w.Themes, nil
	}
	var arr []MarketItem
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, fmt.Errorf("主题市场格式不正确")
}
