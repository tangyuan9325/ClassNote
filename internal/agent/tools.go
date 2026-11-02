package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"classnote/internal/store"
)

// Toolset 是工具注册表。
//
// 参照 DSH 的做法：工具不是散落在各处的 if-else，而是注册进一个表，
// 每个工具自带 spec（名称/参数/是否只读/需要什么权限）。Agent 只认这张表，
// 插件也只能在表内做交集——不能凭空引入新能力。
type Toolset struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewToolset 创建空表。
func NewToolset() *Toolset { return &Toolset{tools: map[string]Tool{}} }

// Add 注册一个工具（同名覆盖）。
func (t *Toolset) Add(tool Tool) {
	if tool == nil {
		return
	}
	spec := tool.Spec()
	if spec.Name == "" {
		return
	}
	t.mu.Lock()
	t.tools[spec.Name] = tool
	t.mu.Unlock()
}

// Names 返回全部工具名（已排序）。
func (t *Toolset) Names() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.tools))
	for k := range t.tools {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Spec 取某个工具的说明。
func (t *Toolset) Spec(name string) (ToolSpec, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tool, ok := t.tools[name]
	if !ok {
		return ToolSpec{}, false
	}
	return tool.Spec(), true
}

// List 返回给定范围内的工具说明（allowed 为空表示全部）。
func (t *Toolset) List(allowed []string) []ToolSpec {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]ToolSpec, 0, len(t.tools))
	for name, tool := range t.tools {
		if len(allowed) > 0 && !contains(allowed, name) {
			continue
		}
		out = append(out, tool.Spec())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run 执行工具。工具 panic 会被兜住：一个插件的坏代码不该让整堂课的服务挂掉。
func (t *Toolset) Run(ctx context.Context, name string, args map[string]any) (res ToolResult, err error) {
	t.mu.RLock()
	tool, ok := t.tools[name]
	t.mu.RUnlock()
	if !ok {
		return ToolResult{}, fmt.Errorf("没有名为 %s 的工具", name)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("工具 %s 内部异常：%v", name, r)
		}
	}()
	if args == nil {
		args = map[string]any{}
	}
	return tool.Run(ctx, args)
}

// ---- 会话作用域 ----
//
// 工具能读哪一堂课，由宿主决定，不由模型决定。
// 以前工具会读 args["sessionId"]：模型只要编一个 id，就能翻别的班的记录。
// 现在 run 开始时把作用域放进 context，工具只认这个值。

type scopeKey struct{}

// WithSession 把本次运行的课堂作用域放进 ctx。
func WithSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, scopeKey{}, sessionID)
}

func sessionOf(ctx context.Context) string {
	if v, ok := ctx.Value(scopeKey{}).(string); ok {
		return v
	}
	return ""
}

// ---- 宿主接口 ----

// Host 是 Agent 能碰到的那一小部分应用能力。
// 刻意做窄：Agent 只能通过这几个方法影响世界，不给它文件系统或命令行。
type Host interface {
	// Session 取课堂记录；id 为空表示当前正在记录的课堂。
	Session(id string) *store.Session
	// EditNote 改某一页的笔记（空字符串表示不改对应字段）。
	EditNote(sessionID, segID, notes, summary string) error
	// GenerateReport 生成报告，返回落地文件路径（可能为空）。
	GenerateReport(id string) (string, error)
	// Timetable 返回今天的课表摘要。
	Timetable() TimetableInfo
	// Plugins 返回已安装插件摘要。
	Plugins() []PluginInfo
}

// TimetableInfo 是课表工具的输出。
type TimetableInfo struct {
	Subject string
	Source  string
	Items   []TimetableItem
}

// TimetableItem 是一节课。
type TimetableItem struct {
	Start   string
	End     string
	Subject string
	Room    string
	Current bool
}

// PluginInfo 是插件摘要。
type PluginInfo struct {
	ID      string
	Name    string
	Kind    string
	Version string
	Enabled bool
}

// RegisterBuiltin 注册内置工具。
func RegisterBuiltin(ts *Toolset, host Host) {
	if ts == nil || host == nil {
		return
	}
	ts.Add(&overviewTool{host})
	ts.Add(&readPageTool{host})
	ts.Add(&searchTool{host})
	ts.Add(&appendNoteTool{host})
	ts.Add(&updateNoteTool{host})
	ts.Add(&reportTool{host})
	ts.Add(&timetableTool{host})
	ts.Add(&pluginListTool{host})
}

// ---- 只读工具 ----

type overviewTool struct{ host Host }

func (t *overviewTool) Spec() ToolSpec {
	return ToolSpec{
		Name: "lesson.overview", Title: "课堂总览", ReadOnly: true,
		Permission:  "read:lesson",
		Description: "列出这节课的页数、时长、字数与每页一句话要点。不知道从哪看起时先调它。",
	}
}

func (t *overviewTool) Run(ctx context.Context, args map[string]any) (ToolResult, error) {
	sess := t.host.Session(sessionOf(ctx))
	if sess == nil {
		return ToolResult{Content: "当前没有课堂记录。可以先让老师开始记录，或直接基于对话回答。", Err: "没有课堂"}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "课堂：%s · %s\n", sess.Subject, sess.Title)
	fmt.Fprintf(&b, "状态：%s，开始于 %s\n", statusCN(sess.Status), sess.StartedAt.Format("15:04"))
	words := 0
	for _, seg := range sess.Segments {
		words += len([]rune(seg.Transcript))
	}
	fmt.Fprintf(&b, "共 %d 页，转写 %d 字\n", len(sess.Segments), words)
	for _, seg := range sess.Segments {
		head := strings.TrimSpace(seg.Summary)
		if head == "" {
			head = firstLine(seg.Notes)
		}
		if head == "" {
			head = clip(seg.Transcript, 40)
		}
		if head == "" {
			head = "（这一页没有内容）"
		}
		fmt.Fprintf(&b, "第 %d 页：%s\n", seg.Index, clip(head, 90))
	}
	return ToolResult{Content: b.String()}, nil
}

type readPageTool struct{ host Host }

func (t *readPageTool) Spec() ToolSpec {
	return ToolSpec{
		Name: "lesson.read_page", Title: "读某一页", ReadOnly: true,
		Permission:  "read:lesson",
		Description: "读第 N 页的转写、笔记与要点。想知道细节时用它，比总览更完整。",
		Args:        []Arg{{Name: "index", Type: "int", Required: true, Note: "页码，从 1 开始"}},
	}
}

func (t *readPageTool) Run(ctx context.Context, args map[string]any) (ToolResult, error) {
	sess := t.host.Session(sessionOf(ctx))
	if sess == nil {
		return ToolResult{Content: "当前没有课堂记录。", Err: "没有课堂"}, nil
	}
	idx := argInt(args, "index", 1)
	seg := findSeg(sess, idx)
	if seg == nil {
		return ToolResult{Content: fmt.Sprintf("这节课只有 %d 页，没有第 %d 页。", len(sess.Segments), idx), Err: "页码超出范围"}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "第 %d 页（%s）\n", seg.Index, seg.CreatedAt.Format("15:04"))
	if seg.Summary != "" {
		fmt.Fprintf(&b, "要点：%s\n", seg.Summary)
	}
	if len(seg.KeyPoints) > 0 {
		fmt.Fprintf(&b, "关键点：%s\n", strings.Join(seg.KeyPoints, "；"))
	}
	if seg.Notes != "" {
		fmt.Fprintf(&b, "现有笔记：\n%s\n", clip(seg.Notes, 2000))
	}
	if seg.Transcript != "" {
		fmt.Fprintf(&b, "现场转写：\n%s\n", clip(seg.Transcript, 2500))
	}
	if seg.Copying {
		fmt.Fprintf(&b, "（这一页的判断：老师在照读幻灯片%s）\n", seg.CopyEvidence)
	}
	if b.Len() == 0 {
		b.WriteString("这一页还没有内容。")
	}
	return ToolResult{Content: b.String()}, nil
}

type searchTool struct{ host Host }

func (t *searchTool) Spec() ToolSpec {
	return ToolSpec{
		Name: "lesson.search", Title: "在本课里搜", ReadOnly: true,
		Permission:  "read:lesson",
		Description: "按关键词在本课的转写与笔记里搜索，返回命中的页码和上下文。适合回答「老师在哪里讲过 X」。",
		Args:        []Arg{{Name: "q", Type: "string", Required: true, Note: "关键词"}},
	}
}

func (t *searchTool) Run(ctx context.Context, args map[string]any) (ToolResult, error) {
	sess := t.host.Session(sessionOf(ctx))
	if sess == nil {
		return ToolResult{Content: "当前没有课堂记录。", Err: "没有课堂"}, nil
	}
	q := strings.TrimSpace(argStr(args, "q"))
	if q == "" {
		return ToolResult{Content: "需要给一个关键词。", Err: "缺少关键词"}, nil
	}
	var hits []string
	for _, seg := range sess.Segments {
		text := seg.Transcript + "\n" + seg.Notes
		i := strings.Index(text, q)
		if i < 0 {
			continue
		}
		start := i - 60
		if start < 0 {
			start = 0
		}
		rs := []rune(text[start:])
		snippet := string(rs)
		if len(rs) > 160 {
			snippet = string(rs[:160])
		}
		hits = append(hits, fmt.Sprintf("第 %d 页：…%s…", seg.Index, snippet))
		if len(hits) >= 8 {
			break
		}
	}
	if len(hits) == 0 {
		return ToolResult{Content: fmt.Sprintf("本课的转写与笔记里没有出现「%s」。", q)}, nil
	}
	return ToolResult{Content: "命中：\n" + strings.Join(hits, "\n")}, nil
}

// ---- 写工具 ----

type appendNoteTool struct{ host Host }

func (t *appendNoteTool) Spec() ToolSpec {
	return ToolSpec{
		Name: "notes.append", Title: "追加笔记", Permission: "write:lesson",
		Description: "在第 N 页现有笔记后追加一段内容，不覆盖原有笔记。",
		Args: []Arg{
			{Name: "index", Type: "int", Required: true, Note: "页码"},
			{Name: "text", Type: "string", Required: true, Note: "要追加的 Markdown 文本"},
			{Name: "preview", Type: "string", Note: "给老师确认用的一句话说明"},
		},
	}
}

func (t *appendNoteTool) Run(ctx context.Context, args map[string]any) (ToolResult, error) {
	sess := t.host.Session(sessionOf(ctx))
	if sess == nil {
		return ToolResult{Content: "当前没有课堂记录。", Err: "没有课堂"}, nil
	}
	idx := argInt(args, "index", 0)
	text := strings.TrimSpace(argStr(args, "text"))
	if idx <= 0 || text == "" {
		return ToolResult{Content: "需要 index 与 text 两个参数。", Err: "参数不完整"}, nil
	}
	seg := findSeg(sess, idx)
	if seg == nil {
		return ToolResult{Content: fmt.Sprintf("没有第 %d 页。", idx), Err: "页码超出范围"}, nil
	}
	next := strings.TrimSpace(seg.Notes)
	if next != "" {
		next += "\n\n"
	}
	next += text
	if err := t.host.EditNote(sess.ID, seg.ID, next, ""); err != nil {
		return ToolResult{Content: "写入失败：" + err.Error(), Err: err.Error()}, nil
	}
	return ToolResult{Content: fmt.Sprintf("已写入第 %d 页笔记，现在共 %d 字。", idx, len([]rune(next)))}, nil
}

type updateNoteTool struct{ host Host }

func (t *updateNoteTool) Spec() ToolSpec {
	return ToolSpec{
		Name: "notes.update", Title: "改写笔记", Permission: "write:lesson",
		Description: "用新内容整体替换第 N 页的笔记；会覆盖老师原有内容，仅在确实需要重写时用。",
		Args: []Arg{
			{Name: "index", Type: "int", Required: true, Note: "页码"},
			{Name: "notes", Type: "string", Required: true, Note: "新的笔记正文（Markdown）"},
			{Name: "summary", Type: "string", Note: "可选：同时更新这一页的一句话要点"},
			{Name: "preview", Type: "string", Note: "给老师确认用的一句话说明"},
		},
	}
}

func (t *updateNoteTool) Run(ctx context.Context, args map[string]any) (ToolResult, error) {
	sess := t.host.Session(sessionOf(ctx))
	if sess == nil {
		return ToolResult{Content: "当前没有课堂记录。", Err: "没有课堂"}, nil
	}
	idx := argInt(args, "index", 0)
	notes := strings.TrimSpace(argStr(args, "notes"))
	if idx <= 0 || notes == "" {
		return ToolResult{Content: "需要 index 与 notes 两个参数。", Err: "参数不完整"}, nil
	}
	seg := findSeg(sess, idx)
	if seg == nil {
		return ToolResult{Content: fmt.Sprintf("没有第 %d 页。", idx), Err: "页码超出范围"}, nil
	}
	if err := t.host.EditNote(sess.ID, seg.ID, notes, strings.TrimSpace(argStr(args, "summary"))); err != nil {
		return ToolResult{Content: "写入失败：" + err.Error(), Err: err.Error()}, nil
	}
	return ToolResult{Content: fmt.Sprintf("第 %d 页笔记已更新。", idx)}, nil
}

type reportTool struct{ host Host }

func (t *reportTool) Spec() ToolSpec {
	return ToolSpec{
		Name: "report.generate", Title: "生成报告", Permission: "write:report",
		Description: "为当前课堂生成图文报告（HTML + PDF）。下课后才用；生成需要十几秒。",
	}
}

func (t *reportTool) Run(ctx context.Context, args map[string]any) (ToolResult, error) {
	path, err := t.host.GenerateReport(sessionOf(ctx))
	if err != nil {
		return ToolResult{Content: "生成失败：" + err.Error(), Err: err.Error()}, nil
	}
	if path == "" {
		return ToolResult{Content: "报告已生成。"}, nil
	}
	return ToolResult{Content: "报告已生成：" + path}, nil
}

// ---- 环境工具 ----

type timetableTool struct{ host Host }

func (t *timetableTool) Spec() ToolSpec {
	return ToolSpec{
		Name: "timetable.today", Title: "今日课表", ReadOnly: true, Permission: "read:plan",
		Description: "读今天的课表与当前节课，用于回答「下节是什么课」「今天还有几节」。",
	}
}

func (t *timetableTool) Run(ctx context.Context, args map[string]any) (ToolResult, error) {
	info := t.host.Timetable()
	if len(info.Items) == 0 {
		return ToolResult{Content: "没有读到课表。可能没装 ClassIsland，或没有配置手动课表。"}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "课表来源：%s\n", info.Source)
	for _, it := range info.Items {
		mark := " "
		if it.Current {
			mark = "←"
		}
		fmt.Fprintf(&b, "%s-%s %s %s %s\n", it.Start, it.End, it.Subject, it.Room, mark)
	}
	return ToolResult{Content: b.String()}, nil
}

type pluginListTool struct{ host Host }

func (t *pluginListTool) Spec() ToolSpec {
	return ToolSpec{
		Name: "plugin.list", Title: "已装插件", ReadOnly: true,
		Description: "列出本机安装的插件与启用状态。老师问「你都会什么」时可以用。",
	}
}

func (t *pluginListTool) Run(ctx context.Context, args map[string]any) (ToolResult, error) {
	list := t.host.Plugins()
	if len(list) == 0 {
		return ToolResult{Content: "本机没有安装插件。"}, nil
	}
	var b strings.Builder
	for _, p := range list {
		state := "停用"
		if p.Enabled {
			state = "启用"
		}
		fmt.Fprintf(&b, "- %s（%s %s）%s\n", p.Name, p.Kind, p.Version, state)
	}
	return ToolResult{Content: b.String()}, nil
}

// ---- 小工具 ----

func findSeg(sess *store.Session, idx int) *store.Segment {
	if sess == nil {
		return nil
	}
	for _, seg := range sess.Segments {
		if seg.Index == idx {
			return seg
		}
	}
	return nil
}

func statusCN(s string) string {
	switch s {
	case "recording":
		return "正在记录"
	case "ended":
		return "已结束"
	default:
		return s
	}
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#-* "))
		if line != "" {
			return line
		}
	}
	return ""
}

func argStr(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case string:
			return strings.TrimSpace(x)
		case fmt.Stringer:
			return strings.TrimSpace(x.String())
		}
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// SleepCtx 是可取消的等待，工具里需要停顿（例如等报告落盘）时用。
func SleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
