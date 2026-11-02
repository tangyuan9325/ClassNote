//go:build windows

package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"classnote/internal/config"
	"classnote/internal/integration"
	"classnote/internal/lesson"
	"classnote/internal/paths"
	"classnote/internal/store"
)

// newSmokeServer 起一个跑在临时目录里的完整服务端，前端资源用仓库里的 web 目录。
func newSmokeServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	p := paths.Paths{
		Root:     root,
		Config:   filepath.Join(root, "config.json"),
		Sessions: filepath.Join(root, "sessions"),
		Captures: filepath.Join(root, "captures"),
		Reports:  filepath.Join(root, "reports"),
		Logs:     filepath.Join(root, "logs"),
		WebCache: filepath.Join(root, "webcache"),
		Bin:      filepath.Join(root, "bin"),
		Runtime:  filepath.Join(root, "runtime"),
	}
	for _, d := range []string{p.Sessions, p.Captures, p.Reports, p.Logs, p.WebCache, p.Bin, p.Runtime} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	st := store.New(p.Sessions)
	cfgNow := cfg.Get()
	eng := lesson.New(p, st, cfgNow)
	ci := integration.NewClassIsland(cfgNow.Integ.ClassIsland, nil, nil)
	icc := integration.NewICCBridge(cfgNow.Integ.ICC)
	web := os.DirFS(filepath.Join("..", "..", "web"))
	return New(p, cfg, st, eng, ci, icc, web)
}

// call 发一次请求并把响应体解析成 map（空响应体时返回 nil）。
func call(t *testing.T, s *Server, method, target string, body any) (int, map[string]any, string) {
	t.Helper()
	var buf *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = bytes.NewReader(raw)
	} else {
		buf = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, buf)
	req.RemoteAddr = "127.0.0.1:51234"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	out := rec.Body.String()
	var m map[string]any
	if len(out) > 0 && out[0] == '{' {
		_ = json.Unmarshal([]byte(out), &m)
	}
	return rec.Code, m, out
}

// TestSmokeCoreRoutes 覆盖老师每天都会用到的老接口：页面、状态、会话、配置。
func TestSmokeCoreRoutes(t *testing.T) {
	s := newSmokeServer(t)

	if code, _, body := call(t, s, "GET", "/", nil); code != 200 || !bytes.Contains([]byte(body), []byte("ClassNote")) {
		t.Fatalf("首页打不开：%d", code)
	}
	if code, m, _ := call(t, s, "GET", "/api/bootstrap", nil); code != 200 || m["config"] == nil {
		t.Fatalf("bootstrap 异常：%d %v", code, m)
	}
	if code, m, _ := call(t, s, "GET", "/api/status", nil); code != 200 || m["status"] == nil {
		t.Fatalf("status 异常：%d %v", code, m)
	}
	if code, _, _ := call(t, s, "GET", "/api/sessions", nil); code != 200 {
		t.Fatalf("sessions 异常：%d", code)
	}
	if code, _, _ := call(t, s, "GET", "/api/themes", nil); code != 200 {
		t.Fatalf("themes 异常：%d", code)
	}
	if code, _, _ := call(t, s, "GET", "/api/provider/presets", nil); code != 200 {
		t.Fatalf("presets 异常：%d", code)
	}
	if code, _, _ := call(t, s, "GET", "/api/timetable", nil); code != 200 {
		t.Fatalf("timetable 异常：%d", code)
	}
	// 静态资源必须能取到，否则页面白屏。
	for _, p := range []string{"/assets/js/app.js", "/assets/css/dsh.css", "/assets/js/shell.js", "/assets/js/agent.js"} {
		if code, _, _ := call(t, s, "GET", p, nil); code != 200 {
			t.Fatalf("静态资源 %s 取不到：%d", p, code)
		}
	}
}

// TestSmokePluginV2 覆盖插件 v2 的清单、市场双渠道、槽位与安装。
func TestSmokePluginV2(t *testing.T) {
	s := newSmokeServer(t)

	code, m, _ := call(t, s, "GET", "/api/plugins", nil)
	if code != 200 {
		t.Fatalf("插件清单异常：%d", code)
	}
	list, _ := m["skills"].([]any)
	if len(list) == 0 {
		t.Fatal("内置插件没有播种")
	}
	for _, it := range list {
		p := it.(map[string]any)
		if p["schemaVersion"].(float64) != 2 {
			t.Fatalf("插件 %v 没有升级到 schemaVersion 2", p["id"])
		}
	}

	// ClassNote 渠道：必须离线可用。
	code, m, _ = call(t, s, "GET", "/api/plugins/market?channel=classnote", nil)
	if code != 200 {
		t.Fatalf("classnote 市场异常：%d", code)
	}
	if items, _ := m["items"].([]any); len(items) == 0 {
		t.Fatal("classnote 渠道没有任何条目")
	}
	if chans, _ := m["channels"].([]any); len(chans) != 2 {
		t.Fatalf("渠道应为 2 个，实际 %d", len(chans))
	}

	// DSH 渠道：走内置快照，至少上千条且每条都有来源地址。
	code, m, _ = call(t, s, "GET", "/api/plugins/market?channel=dsh&limit=50", nil)
	if code != 200 {
		t.Fatalf("dsh 市场异常：%d", code)
	}
	dshItems, _ := m["items"].([]any)
	if len(dshItems) == 0 {
		t.Fatal("dsh 渠道没有返回条目")
	}
	for _, it := range dshItems {
		p := it.(map[string]any)
		if p["kind"] != "external" {
			t.Fatalf("dsh 条目 %v 的 kind 不是 external", p["id"])
		}
		if u, _ := p["url"].(string); u == "" {
			t.Fatalf("dsh 条目 %v 没有来源地址", p["id"])
		}
	}
	if total, _ := m["total"].(float64); total < 1000 {
		t.Fatalf("dsh 索引只有 %.0f 条，快照可能没打进去", total)
	}

	// 槽位接口（新 UI 靠它渲染状态条）。
	if code, m, _ := call(t, s, "GET", "/api/plugins/slots", nil); code != 200 || m["slots"] == nil {
		t.Fatalf("槽位接口异常：%d", code)
	}

	// 开关插件后提示词必须立刻刷新（旧实现要重启才生效）。
	code, _, _ = call(t, s, "POST", "/api/plugins/toggle", map[string]any{"id": "note-assistant", "enabled": false})
	if code != 200 {
		t.Fatalf("停用插件失败：%d", code)
	}
	code, _, _ = call(t, s, "POST", "/api/plugins/toggle", map[string]any{"id": "note-assistant", "enabled": true})
	if code != 200 {
		t.Fatalf("启用插件失败：%d", code)
	}
	// 插件提示词必须在开关后立刻同步给分析引擎（旧实现要重启才生效）。
	if got := s.engine.SkillPrompt(); got == "" {
		t.Fatal("启用插件后引擎没有拿到提示词")
	}
}

// TestSmokePluginRejectsBadInput 确认非法输入被挡在 400，而不是写坏磁盘。
func TestSmokePluginRejectsBadInput(t *testing.T) {
	s := newSmokeServer(t)

	bad := []map[string]any{
		{"id": "../../etc/passwd", "name": "x"},
		{"id": "ok-id", "name": "x", "kind": "external", "source": "file:///etc/passwd"},
		{"id": "ok-id", "name": "x", "tools": []string{"shell.exec"}},
		{"id": "ok-id", "name": "x", "ui": map[string]any{"slot": "sidebar", "title": "t"}},
		{"id": "OK_ID", "name": "x"},
	}
	for _, b := range bad {
		if code, _, _ := call(t, s, "POST", "/api/plugins/save", b); code != 400 {
			t.Fatalf("应拒绝 %v，实际 %d", b, code)
		}
	}
	if code, _, _ := call(t, s, "POST", "/api/plugins/toggle", map[string]any{"id": "no-such"}); code != 404 {
		t.Fatalf("未知插件应为 404，实际 %d", code)
	}
	// 路径穿越的会话 id 不能读到任意文件。
	if code, _, _ := call(t, s, "GET", "/api/session?id=..%2F..%2Fconfig", nil); code == 200 {
		t.Fatal("路径穿越的会话 id 被接受了")
	}
}

// TestSmokeAgentAPI 验证 Agent 的模型目录、权限校验与参数边界。
func TestSmokeAgentAPI(t *testing.T) {
	s := newSmokeServer(t)

	code, m, _ := call(t, s, "GET", "/api/agent/info", nil)
	if code != 200 {
		t.Fatalf("agent info 异常：%d", code)
	}
	models, _ := m["models"].([]any)
	if len(models) < 3 {
		t.Fatalf("模型目录太少：%d", len(models))
	}
	var hasQwen, hasDS bool
	for _, it := range models {
		id, _ := it.(map[string]any)["id"].(string)
		if id == "web:qwen" {
			hasQwen = true
		}
		if id == "web:deepseek" {
			hasDS = true
		}
	}
	if !hasQwen || !hasDS {
		t.Fatalf("模型目录缺少网页版 Qwen/DeepSeek：%v", m["models"])
	}
	if tools, _ := m["toolNames"].([]any); len(tools) < 7 {
		t.Fatalf("工具太少：%d", len(tools))
	}

	// 非法模型 / 步数必须 400。
	if code, _, _ := call(t, s, "POST", "/api/agent/permission", map[string]any{"model": "api:../../evil"}); code != 400 {
		t.Fatalf("非法模型应 400，实际 %d", code)
	}
	if code, _, _ := call(t, s, "POST", "/api/agent/permission", map[string]any{"maxSteps": 99}); code != 400 {
		t.Fatalf("超范围步数应 400，实际 %d", code)
	}
	if code, _, _ := call(t, s, "POST", "/api/agent/permission", map[string]any{"permission": "root"}); code != 400 {
		t.Fatalf("非法权限级别应 400，实际 %d", code)
	}
	// 合法设置要能落盘。
	code, m, _ = call(t, s, "POST", "/api/agent/permission", map[string]any{"permission": "remind", "maxSteps": 8})
	if code != 200 || m["maxSteps"].(float64) != 8 {
		t.Fatalf("保存 Agent 设置失败：%d %v", code, m)
	}

	// 空问题 / 超长问题必须 400，且不能把单飞标志卡死。
	if code, _, _ := call(t, s, "POST", "/api/agent/run", map[string]any{"question": "   "}); code != 400 {
		t.Fatalf("空问题应 400，实际 %d", code)
	}
	long := make([]rune, 2001)
	for i := range long {
		long[i] = '课'
	}
	if code, _, _ := call(t, s, "POST", "/api/agent/run", map[string]any{"question": string(long)}); code != 400 {
		t.Fatalf("超长问题应 400，实际 %d", code)
	}
	if s.agentBusy.Load() {
		t.Fatal("单飞标志没有被释放，后续提问会一直 429")
	}
	// 审批一个不存在的 id 必须 404，不能无限挂住。
	if code, _, _ := call(t, s, "POST", "/api/agent/approve", map[string]any{"id": "ap-不存在", "ok": true}); code != 404 {
		t.Fatalf("未知审批应 404，实际 %d", code)
	}
}

// TestSmokeUIConfig 验证样式切换器写进配置并被 normalize 保留。
func TestSmokeUIConfig(t *testing.T) {
	s := newSmokeServer(t)

	for _, style := range []string{"dsh", "legacy"} {
		code, m, _ := call(t, s, "POST", "/api/config", map[string]any{"ui": map[string]any{"style": style}})
		if code != 200 {
			t.Fatalf("切到 %s 失败：%d %v", style, code, m)
		}
		if got := s.cfg.Get().UI.Style; got != style {
			t.Fatalf("样式没保存：想要 %s，实际 %s", style, got)
		}
	}
	// 非法值要回落到 legacy，而不是把页面搞成第三种状态。
	if _, err := s.cfg.Update(func(c *config.Config) error {
		c.UI.Style = "dos"
		return nil
	}); err != nil {
		t.Fatalf("写入非法样式时不该报错（应被 normalize 兜住）：%v", err)
	}
	if got := s.cfg.Get().UI.Style; got != "legacy" {
		t.Fatalf("非法样式应回落 legacy，实际 %s", got)
	}
}
