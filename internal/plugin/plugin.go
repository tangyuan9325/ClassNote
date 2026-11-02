// Package plugin 管理插件（schemaVersion 2）。
//
// 插件是一个 JSON 文件，放在 plugins/ 下：
//
//	{
//	  "schemaVersion": 2,
//	  "id": "poem-analysis",
//	  "name": "古诗文赏析",
//	  "version": "1.2.0",
//	  "kind": "skill",
//	  "description": "把古诗文按意象/手法/情感拆开讲",
//	  "prompt": "分析古诗文时按意象、手法、情感三层展开……",
//	  "tools": ["lesson.read_page", "notes.append"],
//	  "capabilities": ["prompt", "tools"],
//	  "permissions": ["read:lesson", "write:lesson"],
//	  "events": ["analysis.done"]
//	}
//
// 设计说明见 spec.go：ClassNote 的插件是**声明式**的，只声明能力面与权限，
// 由宿主解释执行。第三方代码永远不会在 ClassNote 进程里运行。
package plugin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Registry 是插件仓库。
type Registry struct {
	mu      sync.RWMutex
	dir     string
	plugins []Plugin
	broken  []Broken
	gen     int
}

// Broken 是一条读不进来的插件文件，界面要能告诉用户为什么。
type Broken struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Open 打开（或创建）插件目录。
func Open(dir string) (*Registry, error) {
	if dir == "" {
		return nil, fmt.Errorf("未指定插件目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &Registry{dir: dir}
	r.reload()
	if len(r.plugins) == 0 {
		r.seed()
	}
	return r, nil
}

// Empty 返回一个不落盘的空仓库。
// 插件目录创建失败时用它兜底，避免 /api/plugins 系列接口 nil 解引用直接 500。
func Empty() *Registry { return &Registry{dir: ""} }

// validID 限制 id 只允许小写字母、数字、点、下划线、连字符。
// 之前 id 直接拼进文件路径，传 "../../config" 就能写到目录外或删掉任意 .json。
func validID(id string) bool { return idPattern.MatchString(id) }

// Dir 返回目录。
func (r *Registry) Dir() string { return r.dir }

// List 列出全部插件。
func (r *Registry) List() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, len(r.plugins))
	copy(out, r.plugins)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Broken 返回读取失败的插件文件。
func (r *Registry) Broken() []Broken {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Broken, len(r.broken))
	copy(out, r.broken)
	return out
}

// Get 按 id 取插件。
func (r *Registry) Get(id string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.plugins {
		if p.ID == id {
			return p, true
		}
	}
	return Plugin{}, false
}

// Enabled 返回启用中且真正生效的插件（外部插件不参与提示词与工具）。
func (r *Registry) Enabled() []Plugin {
	var out []Plugin
	for _, p := range r.List() {
		if p.Enabled && p.Kind != KindExternal {
			out = append(out, p)
		}
	}
	return out
}

// Save 校验并保存/覆盖一个插件。
func (r *Registry) Save(p Plugin) error {
	p.Normalize()
	if err := p.Validate(); err != nil {
		return err
	}
	if r.dir == "" {
		return fmt.Errorf("插件目录不可用")
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.dir, p.ID+".json"), raw, 0o644); err != nil {
		return err
	}
	r.reload()
	return nil
}

// SetEnabled 开关插件。
func (r *Registry) SetEnabled(id string, on bool) bool {
	p, ok := r.Get(id)
	if !ok {
		return false
	}
	p.Enabled = on
	if err := r.Save(p); err != nil {
		return false
	}
	return true
}

// Delete 删除插件。
func (r *Registry) Delete(id string) error {
	if !validID(id) {
		return fmt.Errorf("插件 id 非法")
	}
	if r.dir == "" {
		return fmt.Errorf("插件目录不可用")
	}
	if err := os.Remove(filepath.Join(r.dir, id+".json")); err != nil {
		return err
	}
	r.reload()
	return nil
}

// Has 判断插件是否已安装。
func (r *Registry) Has(id string) bool {
	_, ok := r.Get(id)
	return ok
}

// Prompt 把启用中的插件拼成一段系统提示补充。
//
// 只有声明了 prompt 能力的插件才会进来；外部插件（DSH 引用）一律不进提示词，
// 否则等于把别人的营销文案塞进老师课堂上的模型上下文。
func (r *Registry) Prompt() string {
	var b strings.Builder
	n := 0
	for _, p := range r.Enabled() {
		if !p.Has(CapPrompt) || strings.TrimSpace(p.Prompt) == "" {
			continue
		}
		if n == 0 {
			b.WriteString("\n\n【已启用的插件】\n")
		}
		n++
		fmt.Fprintf(&b, "- %s：%s\n  %s\n", p.Name, p.Description, strings.TrimSpace(p.Prompt))
	}
	return b.String()
}

// Tools 返回启用中的插件允许的工具并集。
func (r *Registry) Tools() []string {
	set := map[string]bool{}
	for _, p := range r.Enabled() {
		if !p.Has(CapTools) {
			continue
		}
		for _, t := range p.Tools {
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
	var list []Plugin
	var broken []Broken
	if r.dir != "" {
		entries, _ := os.ReadDir(r.dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
			if err != nil {
				broken = append(broken, Broken{File: e.Name(), Reason: "读不到文件"})
				continue
			}
			var p Plugin
			if err := json.Unmarshal(raw, &p); err != nil {
				broken = append(broken, Broken{File: e.Name(), Reason: "JSON 格式错误"})
				continue
			}
			if p.ID == "" {
				broken = append(broken, Broken{File: e.Name(), Reason: "缺少 id"})
				continue
			}
			if p.Normalize() {
				// 迁移后立刻落盘，下次启动不再重复迁移。
				if raw, e2 := json.MarshalIndent(p, "", "  "); e2 == nil {
					_ = os.WriteFile(filepath.Join(r.dir, e.Name()), raw, 0o644)
				}
			}
			if err := p.Validate(); err != nil {
				broken = append(broken, Broken{File: e.Name(), Reason: err.Error()})
				continue
			}
			list = append(list, p)
		}
	}
	r.mu.Lock()
	r.plugins = list
	r.broken = broken
	r.gen++
	r.mu.Unlock()
}

// seed 写入几个内置插件示例。
func (r *Registry) seed() {
	builtins := []Plugin{
		{
			ID: "text-close-reading", Name: "文本细读", BuiltIn: true, Enabled: true,
			Kind: KindSkill, Version: "1.0.0", Author: "ClassNote 内置",
			Description: "语文课：从关键词句切入，讲清表达效果",
			Prompt:      "分析课文时先抓关键词句，再讲它的表达效果与情感，避免泛泛而谈。",
			Tools:       []string{"lesson.read_page", "notes.append"},
		},
		{
			ID: "formula-derivation", Name: "公式推导", BuiltIn: true, Enabled: true,
			Kind: KindSkill, Version: "1.0.0", Author: "ClassNote 内置",
			Description: "理科课：把公式推导过程拆成一步步可跟的链条",
			Prompt:      "涉及公式时写出推导链条：已知 → 变形 → 代入 → 结论，每步给一句依据。",
			Tools:       []string{"lesson.read_page"},
		},
		{
			ID: "exam-point-tag", Name: "考点标注", BuiltIn: true, Enabled: false,
			Kind: KindSkill, Version: "1.0.0", Author: "ClassNote 内置",
			Description: "把当页内容标注到常见考点/题型",
			Prompt:      "每页笔记末尾补一行「考点」，指出它常考的题型与易错点。",
			Tools:       []string{"lesson.read_page", "notes.append"},
		},
	}
	for _, p := range builtins {
		p.Normalize()
		raw, _ := json.MarshalIndent(p, "", "  ")
		_ = os.WriteFile(filepath.Join(r.dir, p.ID+".json"), raw, 0o644)
	}
	r.reload()
}

// Install 从市场条目安装插件。
func (r *Registry) Install(m MarketItem) (Plugin, error) {
	p := Plugin{
		SchemaVersion: SchemaVersion,
		ID:            m.ID, Name: m.Name, Version: m.Version,
		Author: m.Author, Description: m.Description, Kind: m.Kind,
		Prompt: m.Prompt, Tools: m.Tools, Source: m.URL, Homepage: m.Homepage,
		License: m.License, Category: m.Category,
		Downloads: m.Downloads, Stars: m.Stars,
	}
	if p.Kind == "" {
		p.Kind = KindSkill
	}
	// 外部插件只登记、不启用：它需要 DSH 运行时，启用也没有可执行的东西。
	p.Enabled = p.Kind != KindExternal
	if p.Kind == KindExternal && p.Description == "" {
		p.Description = "DSH 插件（需要 DeepSeek Harness 运行时）"
	}
	if err := r.Save(p); err != nil {
		return Plugin{}, err
	}
	return p, nil
}
