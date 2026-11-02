package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"classnote/internal/lesson"
	"classnote/internal/llm"
	"classnote/internal/logx"
)

// bgCtx 返回服务级后台上下文。进程退出时预热协程随进程结束，无需额外取消。
func (s *Server) bgCtx() context.Context { return context.Background() }

// chatManager 取出网页版的对话管理能力；用的是 API 模型时返回 nil。
func (s *Server) chatManager() llm.ChatManager {
	return llm.ChatManagerOf(s.Client())
}

// primeSubjects 决定给哪些科目准备对话。
//
// 优先用用户勾选的监听科目；一个都没勾时退回 ClassIsland 里的全部科目——
// 那说明用户还没配，先按课表把所有科目准备好更省事。
func (s *Server) primeSubjects() []string {
	cfg := s.cfg.Get()
	out := make([]string, 0, len(cfg.Lesson.Subjects))
	seen := map[string]bool{}
	for _, v := range cfg.Lesson.Subjects {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) > 0 {
		return out
	}
	for _, v := range s.ci.SubjectNames() {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// PrimeChats 在后台为每门课准备固定对话。
//
// 放在启动后与「保存配置」之后调用：这一步做完，整堂课每页都只发材料，
// 不再重复上千字的系统提示。用后台协程是因为每门课要十几秒，不能卡住界面。
func (s *Server) PrimeChats(force bool) {
	m := s.chatManager()
	if m == nil {
		return
	}
	if m.ChatStatus().Busy {
		return
	}
	subjects := s.primeSubjects()
	if len(subjects) == 0 {
		return
	}
	m.SetPriming(true, nil)
	go func() {
		defer m.SetPriming(false, nil)
		res := m.Prewarm(s.bgCtx(), subjects, lesson.SystemPrompt(), force)
		m.SetPriming(false, res)
		ok, fail := 0, 0
		for _, r := range res {
			if r.Action == "failed" || r.Action == "skipped" {
				fail++
			} else {
				ok++
			}
		}
		logx.Infof("webai", "对话准备完成：成功 %d，未完成 %d", ok, fail)
	}()
}

// handleWebChats 返回各科对话的状态。
func (s *Server) handleWebChats(w http.ResponseWriter, r *http.Request) {
	m := s.chatManager()
	if m == nil {
		writeJSON(w, map[string]any{
			"ok": false, "mode": "api", "chats": []any{},
			"hint": "当前用的是 API 模型，不需要管理网页版对话",
		})
		return
	}
	st := m.ChatStatus()
	writeJSON(w, map[string]any{
		"ok": true, "mode": st.Mode, "chats": st.Chats, "busy": st.Busy,
		"file": st.File, "hint": st.Hint, "last": st.Last,
		"subjects":  s.primeSubjects(),
		"available": s.ci.SubjectNames(),
	})
}

// handleWebChatSet 手动指定/清除某门课的对话地址。
func (s *Server) handleWebChatSet(w http.ResponseWriter, r *http.Request) {
	m := s.chatManager()
	if m == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("当前没有使用网页版 AI"))
		return
	}
	var body struct {
		Subject string `json:"subject"`
		URL     string `json:"url"`
		Clear   bool   `json:"clear"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.Subject == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("请先选择学科"))
		return
	}
	if body.Clear {
		m.ClearChat(body.Subject)
		writeJSON(w, map[string]any{"ok": true, "action": "cleared", "subject": body.Subject})
		return
	}
	if err := m.SetChat(body.Subject, body.URL); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	logx.Infof("webai", "手动指定 %s 的对话地址", body.Subject)
	writeJSON(w, map[string]any{"ok": true, "action": "saved", "subject": body.Subject})
}

// handleWebChatPrime 触发一次对话准备。
func (s *Server) handleWebChatPrime(w http.ResponseWriter, r *http.Request) {
	m := s.chatManager()
	if m == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("当前没有使用网页版 AI"))
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	subjects := s.primeSubjects()
	if len(subjects) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("还没有要监听的科目，请先在课表设置里勾选"))
		return
	}
	s.PrimeChats(body.Force)
	writeJSON(w, map[string]any{"ok": true, "subjects": subjects, "started": true})
}

// handleCISubjects 返回 ClassIsland 里的科目名，供界面做多选。
func (s *Server) handleCISubjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"subjects": s.ci.SubjectNames(),
		"source":   s.ci.SubjectSource(),
		"state":    s.ci.State(),
	})
}
