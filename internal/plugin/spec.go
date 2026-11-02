package plugin

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// SchemaVersion 是当前插件清单的版本号。
//
// v1 是老的「技能包」：一个 JSON，字段只有 id/name/prompt/tools。
// v2 参照 DeepSeek Harness 的插件模型，把插件拆成「声明 + 能力面 + 权限」：
//
//	schemaVersion 2
//	id/name/version/author/kind      身份
//	prompt/tools/ui/events           能力面（插件能做什么）
//	permissions                      权限面（允许碰什么）
//
// 与 DSH 的差别是刻意的：ClassNote 是单二进制 Go 程序，不能让第三方代码
// 直接跑在进程里（那等于把老师的电脑交给 npm 包）。所以这里的插件是
// **声明式**的——插件声明「要注入哪段提示词、要开放哪几个工具、要占哪个
// UI 槽位」，由宿主解释执行，插件本身没有可执行代码。
const SchemaVersion = 2

// 插件形态。
const (
	// KindSkill 提示词型插件：向课堂助手/Agent 注入一段方法论。
	KindSkill = "skill"
	// KindTool 工具型插件：只开放工具集合，不写提示词。
	KindTool = "tool"
	// KindUI 界面型插件：在界面槽位上挂一块内容。
	KindUI = "ui"
	// KindExternal 外部插件：登记一条 DSH（或其它生态）插件，只做引用与展示，
	// 不参与本机执行——因为它是 Node/TS 包，需要 DSH 运行时。
	KindExternal = "external"
)

// 能力面。插件只能声明这里列出的能力，未知能力一律拒绝。
const (
	CapPrompt = "prompt" // 注入提示词
	CapTools  = "tools"  // 开放工具
	CapUI     = "ui"     // 占用界面槽位
	CapEvents = "events" // 订阅课堂事件（只读通知，不回调代码）
)

// 权限面。工具执行前会对照插件声明的权限做闸门检查。
const (
	PermReadLesson  = "read:lesson"  // 读课堂记录、转写、页面笔记
	PermWriteLesson = "write:lesson" // 改页面笔记
	PermReadReport  = "read:report"  // 读报告
	PermWriteReport = "write:report" // 生成/覆盖报告
	PermReadPlan    = "read:plan"    // 读课表
	PermLLM         = "llm"          // 直接调用模型
	PermUI          = "ui"           // 占用界面槽位
)

// 界面槽位。前端只认这几个名字，插件不能自造。
const (
	SlotStatusbar = "statusbar" // 顶栏状态条
	SlotDashboard = "dashboard" // 实录页顶部卡片
	SlotSettings  = "settings"  // 设置抽屉里的插件卡片
	SlotAgent     = "agent"     // Agent 页侧栏提示
)

var (
	validKinds = map[string]bool{KindSkill: true, KindTool: true, KindUI: true, KindExternal: true}
	validCaps  = map[string]bool{CapPrompt: true, CapTools: true, CapUI: true, CapEvents: true}
	validPerms = map[string]bool{
		PermReadLesson: true, PermWriteLesson: true, PermReadReport: true,
		PermWriteReport: true, PermReadPlan: true, PermLLM: true, PermUI: true,
	}
	validSlots = map[string]bool{SlotStatusbar: true, SlotDashboard: true, SlotSettings: true, SlotAgent: true}
	validTools = map[string]bool{
		"lesson.overview": true, "lesson.read_page": true, "lesson.search": true,
		"notes.append": true, "notes.update": true, "report.generate": true,
		"timetable.today": true, "plugin.list": true, "capture.page": true,
		"notes": true, "capture": true, "timetable": true, "report": true,
	}
	// 课堂事件白名单：插件只能收到这些通知。
	validEvents = map[string]bool{
		"lesson.start": true, "lesson.end": true, "slide.change": true,
		"analysis.done": true, "report.done": true,
	}
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	verPattern  = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}([-+][0-9A-Za-z.-]+)?$`)
	slugPattern = regexp.MustCompile(`[^a-z0-9._-]+`)
)

// UISpec 描述一块界面槽位内容。
type UISpec struct {
	Slot  string `json:"slot"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// Href 可选：点开卡片时跳转的地址，只允许 https。
	Href  string `json:"href,omitempty"`
	Order int    `json:"order,omitempty"`
}

// Plugin 是一个插件（schemaVersion 2）。
//
// JSON 字段名沿用 v1，老配置文件不改一个字节也能读进来；
// 新字段全部带 omitempty，写回时才会出现。
type Plugin struct {
	SchemaVersion int      `json:"schemaVersion"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version,omitempty"`
	Author        string   `json:"author,omitempty"`
	Description   string   `json:"description,omitempty"`
	Kind          string   `json:"kind,omitempty"`
	Prompt        string   `json:"prompt,omitempty"`
	Tools         []string `json:"tools,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	Permissions   []string `json:"permissions,omitempty"`
	UI            *UISpec  `json:"ui,omitempty"`
	Events        []string `json:"events,omitempty"`
	Source        string   `json:"source,omitempty"`
	Homepage      string   `json:"homepage,omitempty"`
	License       string   `json:"license,omitempty"`
	Category      string   `json:"category,omitempty"`
	Enabled       bool     `json:"enabled"`
	BuiltIn       bool     `json:"builtIn,omitempty"`
	Downloads     int      `json:"downloads,omitempty"`
	Stars         int      `json:"stars,omitempty"`
}

// Skill 是 v1 的类型名，保留别名让调用方不用改。
type Skill = Plugin

// Normalize 补齐缺省值并做 v1 → v2 迁移，返回是否有改动。
func (p *Plugin) Normalize() bool {
	changed := false
	if p.SchemaVersion == 0 {
		p.SchemaVersion = 1
		changed = true
	}
	if p.Kind == "" {
		p.Kind = KindSkill
		changed = true
	}
	if p.Version == "" {
		if p.Kind == KindExternal {
			p.Version = ""
		} else {
			p.Version = "1.0.0"
			changed = true
		}
	}
	if len(p.Capabilities) == 0 {
		var caps []string
		if strings.TrimSpace(p.Prompt) != "" {
			caps = append(caps, CapPrompt)
		}
		if len(p.Tools) > 0 {
			caps = append(caps, CapTools)
		}
		if p.UI != nil {
			caps = append(caps, CapUI)
		}
		if len(p.Events) > 0 {
			caps = append(caps, CapEvents)
		}
		p.Capabilities = caps
		changed = true
	}
	if len(p.Permissions) == 0 && p.Kind != KindExternal {
		p.Permissions = defaultPermissions(p)
		changed = true
	}
	if p.SchemaVersion < SchemaVersion {
		p.SchemaVersion = SchemaVersion
		changed = true
	}
	return changed
}

// defaultPermissions 按能力面推出最小权限集，不额外授予任何东西。
func defaultPermissions(p *Plugin) []string {
	perms := make([]string, 0, 4)
	if strings.TrimSpace(p.Prompt) != "" || len(p.Tools) > 0 {
		perms = append(perms, PermReadLesson)
	}
	for _, t := range p.Tools {
		switch t {
		case "notes.update", "notes.append":
			perms = append(perms, PermWriteLesson)
		case "report.generate":
			perms = append(perms, PermWriteReport, PermReadReport)
		case "lesson.search":
			perms = append(perms, PermReadReport)
		case "timetable.today":
			perms = append(perms, PermReadPlan)
		}
	}
	if p.UI != nil {
		perms = append(perms, PermUI)
	}
	return dedupe(perms)
}

// Validate 检查插件清单；错误信息直接给用户看，所以要说人话。
func (p *Plugin) Validate() error {
	if !idPattern.MatchString(p.ID) {
		return fmt.Errorf("插件 id 只能是小写字母、数字、点、下划线、连字符，且以字母或数字开头")
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("插件缺少名称")
	}
	if utf8.RuneCountInString(p.Name) > 40 {
		return fmt.Errorf("插件名称过长（最多 40 字）")
	}
	if p.Version != "" && !verPattern.MatchString(p.Version) {
		return fmt.Errorf("版本号格式不正确，应形如 1.0.0")
	}
	if p.Kind == "" {
		p.Kind = KindSkill
	}
	if !validKinds[p.Kind] {
		return fmt.Errorf("未知插件形态 %q（支持 skill / tool / ui / external）", p.Kind)
	}
	for _, c := range p.Capabilities {
		if !validCaps[c] {
			return fmt.Errorf("未知能力面 %q", c)
		}
	}
	for _, perm := range p.Permissions {
		if !validPerms[perm] {
			return fmt.Errorf("未知权限 %q", perm)
		}
	}
	if utf8.RuneCountInString(p.Prompt) > 8000 {
		return fmt.Errorf("提示词过长（最多 8000 字）")
	}
	if len(p.Tools) > 16 {
		return fmt.Errorf("一个插件最多开放 16 个工具")
	}
	for _, t := range p.Tools {
		if !validTools[t] {
			return fmt.Errorf("未知工具 %q", t)
		}
	}
	if p.UI != nil {
		if !validSlots[p.UI.Slot] {
			return fmt.Errorf("未知界面槽位 %q（支持 statusbar / dashboard / settings / agent）", p.UI.Slot)
		}
		if utf8.RuneCountInString(p.UI.Body) > 2000 {
			return fmt.Errorf("界面槽位内容过长（最多 2000 字）")
		}
		if p.UI.Href != "" && !isHTTPS(p.UI.Href) {
			return fmt.Errorf("界面槽位链接只允许 https")
		}
	}
	for _, e := range p.Events {
		if !validEvents[e] {
			return fmt.Errorf("未知事件 %q", e)
		}
	}
	switch p.Kind {
	case KindSkill:
		if strings.TrimSpace(p.Prompt) == "" && len(p.Tools) == 0 {
			return fmt.Errorf("技能型插件至少要有一段提示词或一个工具")
		}
	case KindExternal:
		if !isHTTPS(p.Source) && !isHTTPS(p.Homepage) {
			return fmt.Errorf("外部插件需要提供 https 的来源地址")
		}
	}
	return nil
}

// Has 判断插件是否声明了某个能力面。
func (p *Plugin) Has(capability string) bool {
	for _, c := range p.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Allowed 判断插件是否被授予了某个权限。
func (p *Plugin) Allowed(perm string) bool {
	for _, x := range p.Permissions {
		if x == perm {
			return true
		}
	}
	return false
}

// SanitizeID 把任意字符串压成合法 id，用于从 DSH 仓库名生成插件 id。
func SanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "/", ".")
	s = slugPattern.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "plugin"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	if s[0] >= '0' && s[0] <= '9' {
		// id 允许以数字开头，这里只做长度兜底
		return s
	}
	return s
}

func isHTTPS(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != "" && u.User == nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
