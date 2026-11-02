// Package plugin 管理 Agent 技能包（本地插件）。
//
// 技能包是一个 JSON 文件，放在 plugins/skills/ 下：
//
//	{
//	  "id": "poem-analysis",
//	  "name": "古诗文赏析",
//	  "description": "把古诗文按意象/手法/情感拆开讲",
//	  "prompt": "分析古诗文时按意象、手法、情感三层展开……",
//	  "tools": ["capture", "notes"],
//	  "enabled": true
//	}
//
// 启用后其 prompt 会注入到课堂助手的系统提示里，tools 决定它能调用哪些本地能力。
package plugin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed skills.json
var bundledSkills []byte

// Skill 是一个技能包。
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools,omitempty"` // capture | notes | timetable | report
	Enabled     bool     `json:"enabled"`
	Author      string   `json:"author,omitempty"`
	BuiltIn     bool     `json:"builtIn,omitempty"`
	Downloads   int      `json:"downloads,omitempty"`
	Stars       int      `json:"stars,omitempty"`
}

// Registry 是技能包仓库。
type Registry struct {
	mu    sync.RWMutex
	dir   string
	skill []Skill
}

// Open 打开（或创建）技能目录。
func Open(dir string) (*Registry, error) {
	if dir == "" {
		return nil, fmt.Errorf("未指定插件目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &Registry{dir: dir}
	r.reload()
	if len(r.skill) == 0 {
		r.seed()
	}
	return r, nil
}

// Empty 返回一个不落盘的空仓库。
// 技能目录创建失败时用它兜底，避免 /api/plugins 系列接口 nil 解引用直接 500。
func Empty() *Registry { return &Registry{dir: ""} }

// validID 限制 id 只允许字母、数字、点、下划线、连字符。
// 之前 id 直接拼进文件路径，传 "../../config" 就能写到目录外或删掉任意 .json。
func validID(id string) bool {
	if id == "" || len(id) > 64 || strings.HasPrefix(id, ".") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return !strings.Contains(id, "..")
}

// Dir 返回目录。
func (r *Registry) Dir() string { return r.dir }

// List 列出全部技能包。
func (r *Registry) List() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, len(r.skill))
	copy(out, r.skill)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Enabled 返回启用中的技能包。
func (r *Registry) Enabled() []Skill {
	var out []Skill
	for _, s := range r.List() {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out
}

// Save 保存/覆盖一个技能包。
func (r *Registry) Save(s Skill) error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("技能包缺少 id")
	}
	if !validID(s.ID) {
		return fmt.Errorf("技能包 id 只能包含字母、数字、点、下划线与连字符")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("技能包缺少名称")
	}
	if r.dir == "" {
		return fmt.Errorf("技能包目录不可用")
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.dir, s.ID+".json"), raw, 0o644); err != nil {
		return err
	}
	r.reload()
	return nil
}

// SetEnabled 开关技能包。
func (r *Registry) SetEnabled(id string, on bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.skill {
		if r.skill[i].ID == id {
			r.skill[i].Enabled = on
			raw, _ := json.MarshalIndent(r.skill[i], "", "  ")
			if r.dir != "" && validID(id) {
				_ = os.WriteFile(filepath.Join(r.dir, id+".json"), raw, 0o644)
			}
			return true
		}
	}
	return false
}

// Delete 删除技能包。
func (r *Registry) Delete(id string) error {
	if !validID(id) {
		return fmt.Errorf("技能包 id 非法")
	}
	if r.dir == "" {
		return fmt.Errorf("技能包目录不可用")
	}
	if err := os.Remove(filepath.Join(r.dir, id+".json")); err != nil {
		return err
	}
	r.reload()
	return nil
}

// Prompt 把启用中的技能包拼成一段系统提示补充。
func (r *Registry) Prompt() string {
	var b strings.Builder
	enabled := r.Enabled()
	if len(enabled) == 0 {
		return ""
	}
	b.WriteString("\n\n【已启用的技能包】\n")
	for _, s := range enabled {
		fmt.Fprintf(&b, "- %s：%s\n  %s\n", s.Name, s.Description, strings.TrimSpace(s.Prompt))
	}
	return b.String()
}

// Tools 返回启用中的技能包允许的工具并集。
func (r *Registry) Tools() []string {
	set := map[string]bool{}
	for _, s := range r.Enabled() {
		for _, t := range s.Tools {
			set[t] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) reload() {
	entries, _ := os.ReadDir(r.dir)
	var list []Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
		if err != nil {
			continue
		}
		var s Skill
		if err := json.Unmarshal(raw, &s); err != nil || s.ID == "" {
			continue
		}
		list = append(list, s)
	}
	r.mu.Lock()
	r.skill = list
	r.mu.Unlock()
}

// seed 写入几个内置技能包示例。
func (r *Registry) seed() {
	builtins := []Skill{
		{
			ID: "text-close-reading", Name: "文本细读", BuiltIn: true, Enabled: true,
			Description: "语文课：从关键词句切入，讲清表达效果",
			Prompt:      "分析课文时先抓关键词句，再讲它的表达效果与情感，避免泛泛而谈。",
			Tools:       []string{"notes", "capture"},
		},
		{
			ID: "formula-derivation", Name: "公式推导", BuiltIn: true, Enabled: true,
			Description: "理科课：把公式推导过程拆成一步步可跟的链条",
			Prompt:      "涉及公式时写出推导链条：已知 → 变形 → 代入 → 结论，每步给一句依据。",
			Tools:       []string{"notes"},
		},
		{
			ID: "exam-point-tag", Name: "考点标注", BuiltIn: true, Enabled: false,
			Description: "把当页内容标注到常见考点/题型",
			Prompt:      "每页笔记末尾补一行「考点」，指出它常考的题型与易错点。",
			Tools:       []string{"notes", "report"},
		},
	}
	for _, s := range builtins {
		raw, _ := json.MarshalIndent(s, "", "  ")
		_ = os.WriteFile(filepath.Join(r.dir, s.ID+".json"), raw, 0o644)
	}
	r.reload()
}

// MarketItem 是技能包市场里的一条。
type MarketItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools,omitempty"`
	Author      string   `json:"author,omitempty"`
	URL         string   `json:"url,omitempty"`
	Downloads   int      `json:"downloads,omitempty"`
	Stars       int      `json:"stars,omitempty"`
}

var marketCli = &http.Client{Timeout: 15 * time.Second}

// Market 从远端清单拉取技能包市场条目（离线回退到随包清单）。
func Market(registry string) ([]MarketItem, error) {
	if registry == "" {
		registry = "https://raw.githubusercontent.com/tangyuan9325/ClassNote/main/skills.json"
	}
	req, err := http.NewRequest("GET", registry, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ClassNote")
	resp, err := marketCli.Do(req)
	if err != nil {
		return parseSkillMarket(bundledSkills)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return parseSkillMarket(bundledSkills)
	}
	if items, err := parseSkillMarket(raw); err == nil && len(items) > 0 {
		return mergeBundledMeta(items), nil
	}
	return parseSkillMarket(bundledSkills)
}

func parseSkillMarket(raw []byte) ([]MarketItem, error) {
	var w struct {
		Skills []MarketItem `json:"skills"`
	}
	if err := json.Unmarshal(raw, &w); err == nil && len(w.Skills) > 0 {
		return w.Skills, nil
	}
	var arr []MarketItem
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, fmt.Errorf("技能包市场格式不正确")
}

// mergeBundledMeta 用随包清单补全远端缺失的下载量/星标，
// 这样即使 GitHub 上的 skills.json 还是旧格式，界面也能显示完整信息。
func mergeBundledMeta(items []MarketItem) []MarketItem {
	local, err := parseSkillMarket(bundledSkills)
	if err != nil {
		return items
	}
	meta := make(map[string]MarketItem, len(local))
	for _, s := range local {
		meta[s.ID] = s
	}
	for i := range items {
		if m, ok := meta[items[i].ID]; ok {
			if items[i].Downloads == 0 {
				items[i].Downloads = m.Downloads
			}
			if items[i].Stars == 0 {
				items[i].Stars = m.Stars
			}
		}
	}
	return items
}

// Install 从市场条目安装技能包（写入本地，返回安装后的技能包）。
func (r *Registry) Install(m MarketItem) (Skill, error) {
	s := Skill{
		ID: m.ID, Name: m.Name, Description: m.Description,
		Prompt: m.Prompt, Tools: m.Tools, Author: m.Author, Enabled: true,
		Downloads: m.Downloads, Stars: m.Stars,
	}
	if err := r.Save(s); err != nil {
		return Skill{}, err
	}
	return s, nil
}

// Has 判断技能包是否已安装。
func (r *Registry) Has(id string) bool {
	for _, s := range r.List() {
		if s.ID == id {
			return true
		}
	}
	return false
}
