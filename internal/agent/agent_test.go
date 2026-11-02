package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"classnote/internal/config"
	"classnote/internal/llm"
	"classnote/internal/store"
)

// fakeClient 按脚本依次返回预设回复，用来驱动 Agent 循环。
type fakeClient struct {
	mu    sync.Mutex
	texts []string
	calls int
}

func (f *fakeClient) Chat(ctx context.Context, msgs []llm.Message, opt llm.Options) (*llm.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.texts) {
		return &llm.Result{Text: `{"thought":"没有更多剧本","final":"结束"}`}, nil
	}
	t := f.texts[f.calls]
	f.calls++
	return &llm.Result{Text: t, Usage: llm.Usage{TotalTokens: 10}}, nil
}

func (f *fakeClient) Stream(ctx context.Context, msgs []llm.Message, opt llm.Options, onDelta func(string)) (*llm.Result, error) {
	return f.Chat(ctx, msgs, opt)
}

func (f *fakeClient) Describe() string { return "fake" }
func (f *fakeClient) Ready() bool      { return true }

func (f *fakeClient) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeHost 提供最小可用的课堂数据。
type fakeHost struct {
	sess    *store.Session
	edited  string
	plugins []PluginInfo
}

func (h *fakeHost) Session(id string) *store.Session {
	if id == "" || id == "s1" {
		return h.sess
	}
	return nil
}

func (h *fakeHost) EditNote(sessionID, segID, notes, summary string) error {
	h.edited = notes
	if h.sess != nil {
		for _, seg := range h.sess.Segments {
			if seg.ID == segID {
				seg.Notes = notes
			}
		}
	}
	return nil
}

func (h *fakeHost) GenerateReport(id string) (string, error) { return "/tmp/report.pdf", nil }
func (h *fakeHost) Timetable() TimetableInfo                { return TimetableInfo{Source: "manual"} }
func (h *fakeHost) Plugins() []PluginInfo                   { return h.plugins }

func testSession() *store.Session {
	return &store.Session{
		ID: "s1", Subject: "语文", Title: "荷塘月色", Status: "ended",
		Segments: []*store.Segment{
			{ID: "g1", Index: 1, Summary: "作者行踪", Transcript: "沿着荷塘走，心里颇不宁静。"},
			{ID: "g2", Index: 2, Summary: "景物描写", Transcript: "曲曲折折的荷塘上面，弥望的是田田的叶子。"},
		},
	}
}

func newRunner(host Host, texts []string, perm string) (*Runner, *fakeClient, *Toolset) {
	ts := NewToolset()
	RegisterBuiltin(ts, host)
	cfg := config.Default()
	cfg.Agent.Permission = perm
	cfg.Agent.MaxSteps = 4
	fc := &fakeClient{texts: texts}
	r := New(fc, ts, cfg, nil, nil)
	return r, fc, ts
}

func TestAgentFinalWithoutTool(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	r, fc, _ := newRunner(h, []string{`{"thought":"直接回答","final":"这一课讲的是月下荷塘。"}`}, "remind")
	res, err := r.Run(context.Background(), Request{Question: "讲什么"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Answer == "" || res.Stop != "final" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if fc.count() != 1 {
		t.Fatalf("want 1 llm call, got %d", fc.count())
	}
}

func TestAgentCallsReadTool(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	r, _, _ := newRunner(h, []string{
		`{"thought":"先看总览","tool":"lesson.read_page","args":{"index":2}}`,
		`{"thought":"够了","final":"第 2 页在讲荷叶。"}`,
	}, "remind")
	res, err := r.Run(context.Background(), Request{Question: "第 2 页讲什么"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(res.Steps))
	}
	if !strings.Contains(res.Steps[0].Result, "田田的叶子") {
		t.Fatalf("tool result not observed: %q", res.Steps[0].Result)
	}
}

func TestAgentIgnoresModelSessionID(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	r, _, _ := newRunner(h, []string{
		// 模型自己编了一个 sessionId，想读别的班
		`{"tool":"lesson.overview","args":{"sessionId":"another-class"}}`,
		`{"final":"只能看本课。"}`,
	}, "all")
	res, err := r.Run(context.Background(), Request{Question: "看看", SessionID: "s1"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Steps) == 0 || !strings.Contains(res.Steps[0].Result, "共 2 页") {
		t.Fatalf("tool should read the scoped session: %+v", res.Steps)
	}
	if strings.Contains(res.Steps[0].Result, "没有课堂") {
		t.Fatalf("model-provided sessionId must be ignored: %q", res.Steps[0].Result)
	}
}

func TestAgentApprovalDenied(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	r, _, _ := newRunner(h, []string{
		`{"thought":"补一条笔记","tool":"notes.append","args":{"index":1,"text":"补充：作者心情"}}`,
		`{"thought":"老师拒绝了","final":"那我把要点说给你听。"}`,
	}, "remind")
	r.SetApproval(func(ctx context.Context, req ApprovalRequest) (bool, error) {
		if req.Tool != "notes.append" {
			t.Fatalf("unexpected approval tool %s", req.Tool)
		}
		return false, nil
	})
	res, err := r.Run(context.Background(), Request{Question: "补笔记"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if h.edited != "" {
		t.Fatalf("note should not be written when denied, got %q", h.edited)
	}
	if len(res.Steps) != 1 || !res.Steps[0].Skipped {
		t.Fatalf("step should be marked skipped: %+v", res.Steps)
	}
}

func TestAgentApprovalAllowed(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	r, _, _ := newRunner(h, []string{
		`{"thought":"补一条笔记","tool":"notes.append","args":{"index":1,"text":"补充：作者心情"}}`,
		`{"thought":"写好了","final":"已补上。"}`,
	}, "remind")
	r.SetApproval(func(ctx context.Context, req ApprovalRequest) (bool, error) { return true, nil })
	if _, err := r.Run(context.Background(), Request{Question: "补笔记"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(h.edited, "作者心情") {
		t.Fatalf("note not written: %q", h.edited)
	}
}

func TestAgentMaxStepsStops(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	// 一直要调工具，永远不 final；第 5 次才是收尾请求。
	texts := []string{
		`{"tool":"lesson.overview","args":{}}`,
		`{"tool":"lesson.read_page","args":{"index":1}}`,
		`{"tool":"lesson.read_page","args":{"index":2}}`,
		`{"tool":"timetable.today","args":{}}`,
		`{"final":"步数用尽后的总结"}`,
	}
	r, _, _ := newRunner(h, texts, "all")
	res, err := r.Run(context.Background(), Request{Question: "随便看看"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stop != "max-steps" {
		t.Fatalf("want max-steps, got %q", res.Stop)
	}
	if res.Answer == "" {
		t.Fatal("should still produce an answer after max steps")
	}
}

func TestAgentRejectsUnknownTool(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	r, _, _ := newRunner(h, []string{
		`{"tool":"shell.exec","args":{"cmd":"rm -rf /"}}`,
		`{"final":"我不会执行命令。"}`,
	}, "all")
	res, err := r.Run(context.Background(), Request{Question: "删库"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Steps) != 1 || !strings.Contains(res.Steps[0].Error, "不可用") {
		t.Fatalf("unknown tool should be refused: %+v", res.Steps)
	}
}

func TestParseActionTolerance(t *testing.T) {
	cases := []struct {
		in    string
		tool  string
		final string
	}{
		{`{"thought":"t","final":"答案"}`, "", "答案"},
		{"```json\n{\"tool\":\"lesson.overview\",\"args\":{}}\n```", "lesson.overview", ""},
		{`{"action":{"tool":"notes.update","args":{"index":1,"notes":"x"}}}`, "notes.update", ""},
		{`就是一段普通回答`, "", "就是一段普通回答"},
		{`{"content":"字段不对但也是 JSON"}`, "", ""},
	}
	for _, c := range cases {
		a := parseAction(c.in)
		if a.Tool != c.tool {
			t.Errorf("tool: got %q want %q (input %q)", a.Tool, c.tool, c.in)
		}
		if c.final != "" && !strings.Contains(a.Final, c.final) {
			t.Errorf("final: got %q want %q", a.Final, c.final)
		}
	}
}

func TestCatalogHasWebModels(t *testing.T) {
	cfg := config.Default()
	list := Catalog(cfg)
	var hasQwen, hasDS bool
	for _, m := range list {
		if m.ID == ModelWebQwen {
			hasQwen = true
		}
		if m.ID == ModelWebDeepSeek {
			hasDS = true
		}
	}
	if !hasQwen || !hasDS {
		t.Fatalf("catalog must include web qwen & deepseek: %+v", list)
	}
	// 网页版客户端必须能造出来（不需要 Key）。
	if _, err := ClientFor(cfg, ModelWebQwen); err != nil {
		t.Fatalf("client for web qwen: %v", err)
	}
	if _, err := ClientFor(cfg, "nope:xx"); err == nil {
		t.Fatal("unknown model should error")
	}
}

func TestToolsReadAndWrite(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	ts := NewToolset()
	RegisterBuiltin(ts, h)

	res, err := ts.Run(context.Background(), "lesson.overview", map[string]any{})
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if !strings.Contains(res.Content, "共 2 页") {
		t.Fatalf("overview content: %q", res.Content)
	}

	args, _ := json.Marshal(map[string]any{"index": float64(2)})
	var m map[string]any
	_ = json.Unmarshal(args, &m)
	res, err = ts.Run(context.Background(), "lesson.read_page", m)
	if err != nil || !strings.Contains(res.Content, "田田的叶子") {
		t.Fatalf("read_page: %v %q", err, res.Content)
	}

	// 越界页码要给出可读提示，而不是空内容。
	res, _ = ts.Run(context.Background(), "lesson.read_page", map[string]any{"index": float64(9)})
	if !strings.Contains(res.Content, "没有第 9 页") {
		t.Fatalf("out of range should be explained: %q", res.Content)
	}
}

func TestNeedApprovalLevels(t *testing.T) {
	h := &fakeHost{sess: testSession()}
	for _, c := range []struct {
		perm     string
		tool     string
		ask      bool
	}{
		{"all", "notes.append", false},
		{"remind", "notes.append", true},
		{"remind", "lesson.overview", false},
		{"review", "lesson.overview", true},
	} {
		r, _, _ := newRunner(h, nil, c.perm)
		_, ask := r.needApproval(c.tool)
		if ask != c.ask {
			t.Errorf("perm=%s tool=%s ask=%v want %v", c.perm, c.tool, ask, c.ask)
		}
	}
}
