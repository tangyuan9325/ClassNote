package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"classnote/internal/agent"
	"classnote/internal/config"
	"classnote/internal/lesson"
	"classnote/internal/llm"
	"classnote/internal/store"
)

// agentHost 把应用能力暴露给 Agent 工具，边界刻意收窄。
type agentHost struct{ s *Server }

func (h agentHost) Session(id string) *store.Session {
	s := h.s
	if id == "" {
		if copy := s.engine.SessionCopy(); copy != nil {
			return copy
		}
		if sess, err := s.store.List(); err == nil && len(sess) > 0 {
			if full, err := s.store.Load(sess[0].ID); err == nil {
				return full
			}
		}
		return nil
	}
	if copy := s.engine.SessionCopy(); copy != nil && copy.ID == id {
		return copy
	}
	sess, err := s.store.Load(id)
	if err != nil {
		return nil
	}
	return sess
}

func (h agentHost) EditNote(sessionID, segID, notes, summary string) error {
	return h.s.engine.EditNote(sessionID, segID, notes, summary)
}

func (h agentHost) GenerateReport(id string) (string, error) {
	sess := h.Session(id)
	if sess == nil {
		return "", fmt.Errorf("找不到这堂课")
	}
	if err := h.s.engine.GenerateReport(sess); err != nil {
		return "", err
	}
	if sess.Report != nil {
		if sess.Report.PDFPath != "" {
			return sess.Report.PDFPath, nil
		}
		return sess.Report.HTMLPath, nil
	}
	return "", nil
}

func (h agentHost) Timetable() agent.TimetableInfo {
	st := h.s.ci.State()
	info := agent.TimetableInfo{Subject: st.Profile, Source: st.Source}
	curStart := ""
	if st.Current != nil {
		curStart = st.Current.Start.Format("15:04")
		info.Subject = st.Current.Subject
	}
	for _, l := range st.Today {
		info.Items = append(info.Items, agent.TimetableItem{
			Start: l.Start.Format("15:04"), End: l.End.Format("15:04"),
			Subject: l.Subject, Room: l.Room,
			Current: curStart != "" && l.Start.Format("15:04") == curStart,
		})
	}
	if info.Source == "" {
		info.Source = "未配置"
	}
	return info
}

func (h agentHost) Plugins() []agent.PluginInfo {
	out := []agent.PluginInfo{}
	if h.s.plugins == nil {
		return out
	}
	for _, p := range h.s.plugins.List() {
		out = append(out, agent.PluginInfo{
			ID: p.ID, Name: p.Name, Kind: p.Kind, Version: p.Version, Enabled: p.Enabled,
		})
	}
	return out
}

// toolset 惰性构建工具表（进程内单例即可，工具是无状态的）。
func (s *Server) toolset() *agent.Toolset {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	if s.tools != nil {
		return s.tools
	}
	ts := agent.NewToolset()
	agent.RegisterBuiltin(ts, agentHost{s: s})
	s.tools = ts
	return ts
}

// clientForAgent 按 Agent 配置选模型；网页版要复用固定的 profile 目录。
func (s *Server) clientForAgent() (llm.Client, string, error) {
	cfg := s.cfg.Get()
	if cfg.WebAI.ProfileDir == "" {
		cfg.WebAI.ProfileDir = s.paths.WebCache
	}
	cfg.WebAI.ChatsFile = filepath.Join(s.paths.Root, "data", "webai-chats.json")
	id := cfg.Agent.Model
	c, err := agent.ClientFor(cfg, id)
	return c, agent.DescribeModel(id), err
}

// handleAgentInfo 返回 Agent 权限、模型目录与工具清单。
func (s *Server) handleAgentInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	var grants []pluginGrant
	if s.plugins != nil {
		for _, g := range s.plugins.Snapshot().Grants {
			grants = append(grants, pluginGrant{
				PluginID: g.PluginID, Name: g.Name, Kind: g.Kind,
				Perms: g.Perms, Tools: g.Tools,
			})
		}
	}
	writeJSON(w, map[string]any{
		"permission": cfg.Agent.Permission,
		"model":      cfg.Agent.Model,
		"maxSteps":   cfg.Agent.MaxSteps,
		"models":     agent.Catalog(cfg),
		"tools":      s.toolset().List(nil),
		"toolNames":  s.toolset().Names(),
		"grants":     grants,
		"skills":     len(s.plugins.Enabled()),
		"levels": []map[string]string{
			{"id": "all", "name": "全部访问", "note": "Agent 可直接使用全部本地能力，不再询问"},
			{"id": "remind", "name": "敏感操作提醒", "note": "涉及写笔记/生成报告时先确认（推荐）"},
			{"id": "review", "name": "全部人工审查", "note": "每一步操作都要人工确认"},
		},
	})
}

type pluginGrant struct {
	PluginID string   `json:"pluginId"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Perms    []string `json:"perms"`
	Tools    []string `json:"tools"`
}

// handleAgentPermission 修改 Agent 权限级别 / 模型 / 步数。
func (s *Server) handleAgentPermission(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Permission string `json:"permission"`
		Model      string `json:"model"`
		MaxSteps   int    `json:"maxSteps"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Permission != "" && body.Permission != "all" && body.Permission != "remind" && body.Permission != "review" {
		writeErr(w, 400, fmt.Errorf("权限级别只能是 all / remind / review"))
		return
	}
	if body.Model != "" {
		valid := false
		for _, m := range agent.Catalog(s.cfg.Get()) {
			if m.ID == body.Model {
				valid = true
				break
			}
		}
		if !valid {
			writeErr(w, 400, fmt.Errorf("不支持的模型：%s", body.Model))
			return
		}
	}
	if body.MaxSteps < 0 || body.MaxSteps > 12 {
		writeErr(w, 400, fmt.Errorf("步数只能是 1-12"))
		return
	}
	cfg, err := s.cfg.Update(func(c *config.Config) error {
		if body.Permission != "" {
			c.Agent.Permission = body.Permission
		}
		if body.Model != "" {
			c.Agent.Model = body.Model
		}
		if body.MaxSteps > 0 {
			c.Agent.MaxSteps = body.MaxSteps
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "permission": cfg.Agent.Permission, "model": cfg.Agent.Model, "maxSteps": cfg.Agent.MaxSteps})
}

// handleAgentRun 跑一次完整 Agent turn。
//
// 过程事件通过现有的 SSE 通道推给页面（agent_step / agent_tool / …），
// HTTP 响应只回最终结果。写操作会先发 agent_approval，等 /api/agent/approve。
func (s *Server) handleAgentRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question  string `json:"question"`
		SessionID string `json:"sessionId"`
		Model     string `json:"model"`
		MaxSteps  int    `json:"maxSteps"`
		History   []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	q := strings.TrimSpace(body.Question)
	if q == "" {
		writeErr(w, 400, fmt.Errorf("问题不能为空"))
		return
	}
	// 同一时刻只跑一个 turn：老师连点发送键时，不应该同时烧两次模型额度，
	// 也不应该让两个写操作在课堂记录上互相覆盖。
	if !s.agentBusy.CompareAndSwap(false, true) {
		writeErr(w, 429, fmt.Errorf("上一条还在处理，请等它结束"))
		return
	}
	defer s.agentBusy.Store(false)
	if len([]rune(q)) > 2000 {
		writeErr(w, 400, fmt.Errorf("问题太长了（最多 2000 字）"))
		return
	}
	cfg := s.cfg.Get()
	if cfg.WebAI.ProfileDir == "" {
		cfg.WebAI.ProfileDir = s.paths.WebCache
	}
	cfg.WebAI.ChatsFile = filepath.Join(s.paths.Root, "data", "webai-chats.json")

	modelID := cfg.Agent.Model
	if body.Model != "" {
		for _, m := range agent.Catalog(cfg) {
			if m.ID == body.Model {
				modelID = body.Model
				break
			}
		}
	}
	client, err := agent.ClientFor(cfg, modelID)
	if err != nil {
		// 网页版客户端构造不报错但不可用，这里只拦硬错误。
		writeErr(w, 400, err)
		return
	}

	hist := make([]llm.Message, 0, len(body.History))
	for _, h := range body.History {
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		role := llm.RoleAssistant
		if h.Role == "user" {
			role = llm.RoleUser
		}
		hist = append(hist, llm.TextMessage(role, clipRunes(h.Content, 2000)))
	}

	subject := ""
	if body.SessionID != "" {
		if sess, err := s.store.Load(body.SessionID); err == nil {
			subject = sess.Subject
			if strings.TrimSpace(sess.Subject) != "" {
				q = "本课：" + sess.Subject + " " + sess.Title + "\n" + q
			}
		}
	}

	runner := agent.New(client, s.toolset(), cfg, s.plugins, func(typ string, data any) {
		s.broadcast(lesson.Event{Type: typ, Data: data, At: time.Now()})
	})
	runner.SetApproval(s.awaitApproval)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := runner.Run(ctx, agent.Request{
		Question: q, SessionID: body.SessionID, History: hist,
		Subject: subject, MaxSteps: body.MaxSteps,
	})
	if err != nil && (res == nil || res.Answer == "") {
		writeErr(w, 500, err)
		return
	}
	// 对话留档：课堂上问过的内容，下课后在历史里还能看到。
	if body.SessionID != "" && res != nil && res.Answer != "" {
		if !s.engine.AppendChat(body.SessionID, body.Question, res.Answer) {
			if err := s.store.AppendMessage(body.SessionID, "user", body.Question); err == nil {
				_ = s.store.AppendMessage(body.SessionID, "assistant", res.Answer)
			}
		}
	}
	writeJSON(w, map[string]any{
		"ok": true, "answer": res.Answer, "steps": res.Steps,
		"model": res.Model, "stop": res.Stop,
		"elapsedMs": res.ElapsedMS, "usage": res.Usage,
	})
}

// awaitApproval 发一个审批事件并等待页面回话。
func (s *Server) awaitApproval(ctx context.Context, req agent.ApprovalRequest) (bool, error) {
	ch := make(chan bool, 1)
	s.approveMu.Lock()
	if s.approvals == nil {
		s.approvals = map[string]chan bool{}
	}
	s.approvals[req.ID] = ch
	s.approveMu.Unlock()
	defer func() {
		s.approveMu.Lock()
		delete(s.approvals, req.ID)
		s.approveMu.Unlock()
	}()

	timer := time.NewTimer(150 * time.Second)
	defer timer.Stop()
	select {
	case ok := <-ch:
		return ok, nil
	case <-timer.C:
		return false, fmt.Errorf("等待确认超时（150 秒）")
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// handleAgentApprove 接收页面对某次审批的决定。
func (s *Server) handleAgentApprove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Allow bool   `json:"allow"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.ID == "" {
		writeErr(w, 400, fmt.Errorf("缺少审批 id"))
		return
	}
	s.approveMu.Lock()
	ch, ok := s.approvals[body.ID]
	s.approveMu.Unlock()
	if !ok {
		writeErr(w, 404, fmt.Errorf("这次审批已经结束"))
		return
	}
	select {
	case ch <- body.Allow:
	default:
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ToolNames 供自检命令确认工具表能建起来。
func (s *Server) ToolNames() []string { return s.toolset().Names() }

// clipRunes 按字数截断，前端与会话摘要共用。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
