package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"classnote/internal/app"
	"classnote/internal/asr"
	"classnote/internal/audio"
	"classnote/internal/capture"
	"classnote/internal/config"
	"classnote/internal/integration"
	"classnote/internal/lesson"
	"classnote/internal/llm"
	"classnote/internal/logx"
	"classnote/internal/notify"
	"classnote/internal/plugin"
	"classnote/internal/store"
	"classnote/internal/theme"
	"classnote/internal/update"
)

// Theme 是界面主题描述。
type Theme struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Note   string `json:"note"`
	Swatch string `json:"swatch"`
}

func themes() []Theme {
	return []Theme{
		{ID: "claude", Name: "羊皮纸", Note: "米白底 / 陶土红点缀", Swatch: "#C96442"},
		{ID: "graphite", Name: "石墨", Note: "深灰底 / 冷白文字", Swatch: "#8A9BA8"},
		{ID: "mist", Name: "雾蓝", Note: "冷调灰蓝 / 低对比", Swatch: "#5B7C99"},
		{ID: "forest", Name: "松林", Note: "墨绿 / 暖木色", Swatch: "#4E6B51"},
	}
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	_ = s.Client()
	writeJSON(w, map[string]any{
		"app": map[string]any{
			"version": app.Version, "build": app.BuildTime, "platform": app.Platform(),
			"paths": s.paths, "port": s.port, "url": s.URL(), "pid": os.Getpid(),
			"onboarded": cfg.Onboarded,
		},
		"config":        s.cfg.Redacted(),
		"presets":       llm.Presets(),
		"webSites":      llm.WebSites(),
		"themes":        themes(),
		"notifyPresets": notify.Presets(),
		"status":        s.engine.Status(),
		"integrations": map[string]any{
			"classIsland": s.ci.State(),
			"icc":         s.icc.State(),
		},
		"now": time.Now(),
		"server": map[string]any{
			"access": cfg.Server.Access,
			"token":  cfg.Server.Token,
			"urls":   lanURLs(s.port, cfg.Server.Token),
		},
	})
}

// lanURLs 构造局域网访问链接（含密钥，方便复制到手机/平板）。
func lanURLs(port int, token string) []string {
	out := []string{fmt.Sprintf("http://127.0.0.1:%d/", port)}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ip := ipnet.IP.To4()
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			q := ""
			if token != "" {
				q = "?token=" + token
			}
			out = append(out, fmt.Sprintf("http://%s:%d/%s", ip, port, q))
		}
	}
	return out
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, fmt.Errorf("只支持 POST"))
		return
	}
	var incoming config.Config
	if err := readJSON(r, &incoming); err != nil {
		writeErr(w, 400, err)
		return
	}
	cur := s.cfg.Get()
	// 密钥为掩码或空时保留原值（掩码形如 sk-12••••••ab）。
	if maskedOrEmpty(incoming.Provider.APIKey) {
		incoming.Provider.APIKey = cur.Provider.APIKey
	}
	if maskedOrEmpty(incoming.ASR.APIKey) {
		incoming.ASR.APIKey = cur.ASR.APIKey
	}
	if incoming.Onboarded == false {
		incoming.Onboarded = cur.Onboarded
	}
	// 路径为空时保留原值：界面可能只提交部分字段，不该把已配置的路径清掉。
	if incoming.Integ.ClassIsland.DataDir == "" {
		incoming.Integ.ClassIsland.DataDir = cur.Integ.ClassIsland.DataDir
	}
	if incoming.Integ.ClassIsland.ExePath == "" {
		incoming.Integ.ClassIsland.ExePath = cur.Integ.ClassIsland.ExePath
	}
	if incoming.Integ.ICC.ExePath == "" {
		incoming.Integ.ICC.ExePath = cur.Integ.ICC.ExePath
	}
	if incoming.Integ.ICC.PluginDir == "" {
		incoming.Integ.ICC.PluginDir = cur.Integ.ICC.PluginDir
	}
	if incoming.WebAI.ProfileDir == "" {
		incoming.WebAI.ProfileDir = cur.WebAI.ProfileDir
	}
	cfg, err := s.cfg.Update(func(c *config.Config) error {
		*c = incoming
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	s.engine.Update(cfg)
	s.ci.Update(cfg.Integ.ClassIsland, cfg.Lesson.Subjects, cfg.Lesson.ManualTimetable)
	s.icc.Update(cfg.Integ.ICC)
	client, cerr := s.RefreshClient()
	resp := map[string]any{"config": s.cfg.Redacted(), "ok": true}
	if cerr != nil {
		resp["clientError"] = cerr.Error()
	} else if client != nil {
		resp["client"] = client.Describe()
	}
	writeJSON(w, resp)
}

// maskedOrEmpty 判断提交上来的密钥是否只是掩码或空串。
func maskedOrEmpty(key string) bool {
	return key == "" || strings.Contains(key, "•")
}

func (s *Server) handleConfigReset(w http.ResponseWriter, r *http.Request) {
	d := config.Default()
	d.Onboarded = false
	if _, err := s.cfg.Update(func(c *config.Config) error { *c = d; return nil }); err != nil {
		writeErr(w, 500, err)
		return
	}
	cfg := s.cfg.Get()
	s.engine.Update(cfg)
	s.ci.Update(cfg.Integ.ClassIsland, cfg.Lesson.Subjects, cfg.Lesson.ManualTimetable)
	s.icc.Update(cfg.Integ.ICC)
	_, _ = s.RefreshClient()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	type item struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		OK       bool   `json:"ok"`
		Detail   string `json:"detail"`
		Path     string `json:"path,omitempty"`
		Hint     string `json:"hint,omitempty"`
		Optional bool   `json:"optional"`
	}
	var items []item

	// 语音转写（本地 whisper 或云端）
	asrMode := cfg.ASR.Mode
	asrOK := false
	asrDetail := "未启用"
	asrHint := "可在设置里选择本地模型或云端接口"
	if asrMode == "local" {
		asrOK = cfg.ASR.LocalPort > 0 && asr.LocalHealth(cfg.ASR.LocalPort)
		if asrOK {
			asrDetail = fmt.Sprintf("本地 whisper.cpp 运行中（端口 %d，模型 %s）", cfg.ASR.LocalPort, filepath.Base(cfg.ASR.LocalModel))
		} else {
			asrDetail = "本地模型未运行"
			asrHint = "首次启动会自动部署模型到 C:////ProgramData////ClassNote////whisper；也可用 scripts\\get-whisper.ps1 手动下载"
		}
	} else if asrMode == "api" && cfg.ASR.APIKey != "" {
		asrOK = true
		asrDetail = "云端接口 · " + cfg.ASR.Model
	}
	items = append(items, item{
		ID: "asr", Name: "语音转写模型", OK: asrOK, Optional: false,
		Detail: asrDetail, Hint: asrHint,
	})

	// InkCanvasForClass
	icc := s.icc.State()
	items = append(items, item{
		ID: "icc", Name: "InkCanvasForClass CE", OK: icc.Running, Optional: true,
		Detail: orDefault(icc.ExePath, "未运行"), Path: icc.InstallDir,
		Hint: "用于获取放映状态与批注事件；未安装时仍可通过系统接口检测放映",
	})
	// ClassIsland
	ci := s.ci.State()
	items = append(items, item{
		ID: "classisland", Name: "ClassIsland 课表", OK: ci.Detected, Optional: true,
		Detail: orDefault(ci.DataDir, "未找到数据目录"), Path: ci.DataDir,
		Hint: "读取课表判断上下课；也可手动指定数据目录",
	})
	// 办公软件
	ppt := findInstall([]string{
		`C:\Program Files\Microsoft Office\root\Office16\POWERPNT.EXE`,
		`C:\Program Files (x86)\Microsoft Office\root\Office16\POWERPNT.EXE`,
	})
	wps := findInstall([]string{
		`C:\Program Files\Kingsoft\WPS Office\ksolaunch.exe`,
		`C:\Program Files (x86)\Kingsoft\WPS Office\ksolaunch.exe`,
	})
	ppOK := ppt != "" || wps != ""
	detail := "未检测到 PowerPoint / WPS"
	if ppt != "" {
		detail = "Microsoft PowerPoint"
	}
	if wps != "" {
		if detail != "未检测到 PowerPoint / WPS" {
			detail += " + WPS Office"
		} else {
			detail = "WPS Office"
		}
	}
	items = append(items, item{
		ID: "office", Name: "幻灯片放映", OK: ppOK, Optional: false,
		Detail: detail, Path: ppt, Hint: "支持 PowerPoint 与 WPS 演示的放映检测与截图",
	})
	// 麦克风
	n := audio.Devices()
	items = append(items, item{
		ID: "mic", Name: "麦克风", OK: n > 0, Optional: false,
		Detail: fmt.Sprintf("检测到 %d 个录音设备", n),
		Hint:   "课堂录音转写需要麦克风权限",
	})
	// 浏览器 / PDF 引擎
	edge := ""
	for _, p := range []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			edge = p
			break
		}
	}
	items = append(items, item{
		ID: "browser", Name: "Edge 浏览器", OK: edge != "", Optional: false,
		Detail: orDefault(edge, "未找到"), Path: edge,
		Hint: "用于生成 PDF 报告与网页版 AI 自动化",
	})
	// 模型
	client, cerr := llm.New(cfg)
	modelOK := cerr == nil && client != nil
	modelDetail := "未配置"
	if modelOK {
		modelDetail = client.Describe()
	} else if cerr != nil {
		modelDetail = cerr.Error()
	}
	items = append(items, item{
		ID: "model", Name: "AI 模型", OK: modelOK, Optional: false,
		Detail: modelDetail, Hint: "可在设置中切换 API 或网页版",
	})
	writeJSON(w, map[string]any{"items": items, "now": time.Now()})
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func findInstall(paths []string) string {
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"presets": llm.Presets(), "webSites": llm.WebSites()})
}

func (s *Server) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config *config.Config `json:"config"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg := s.cfg.Get()
	if body.Config != nil {
		cfg.Provider = body.Config.Provider
		cfg.WebAI = body.Config.WebAI
		if cfg.Provider.APIKey == "" || strings.HasPrefix(cfg.Provider.APIKey, "•") {
			cfg.Provider.APIKey = s.cfg.Get().Provider.APIKey
		}
	}
	client, err := llm.New(cfg)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res, err := client.Chat(ctx, []llm.Message{
		llm.TextMessage(llm.RoleSystem, "你是连通性测试助手，只输出一行中文短句。"),
		llm.UserText("请回复：连接成功，并附上一句 12 字以内的问候。"),
	}, llm.Options{Temperature: 0.6, MaxTokens: 120})
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "latencyMs": time.Since(start).Milliseconds()})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "reply": res.Text, "model": res.Model, "via": res.Via,
		"latencyMs": time.Since(start).Milliseconds(), "usage": res.Usage,
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	base := strings.TrimRight(cfg.Provider.BaseURL, "/")
	if base == "" {
		writeJSON(w, map[string]any{"models": []string{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if cfg.Provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Provider.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, map[string]any{"models": []string{}, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &out)
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	writeJSON(w, map[string]any{"models": ids})
}

func (s *Server) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	if err := llm.OpenLogin(cfg, s.paths.WebCache); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "已打开浏览器，请完成登录后点击“检测登录状态”"})
}

func (s *Server) handleWebCheck(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	ok, msg, err := llm.LoginCheck(cfg, s.paths.WebCache)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	if ok {
		_, _ = s.cfg.Update(func(c *config.Config) error { c.WebAI.LoggedIn = true; return nil })
	}
	writeJSON(w, map[string]any{"ok": ok, "message": msg})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := s.engine.Status()
	ci := s.ci.State()
	icc := s.icc.State()
	writeJSON(w, map[string]any{
		"status": st,
		"classIsland": map[string]any{
			"inClass": ci.InClass, "preparing": ci.Preparing, "subject": subjectOf(ci.Current),
			"next": subjectOf(ci.Next), "nextStart": nextStart(ci.Next), "monitored": ci.Monitored,
			"source": ci.Source, "running": ci.Running, "error": ci.Error,
			"today": ci.Today,
		},
		"icc": map[string]any{
			"running": icc.Running, "presenting": icc.Presenting, "plugin": icc.PluginInstalled,
		},
		"now": time.Now(),
	})
}

func subjectOf(l *integration.Lesson) string {
	if l == nil {
		return ""
	}
	return l.Subject
}

func nextStart(l *integration.Lesson) string {
	if l == nil {
		return ""
	}
	return l.Start.Format("15:04")
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subject string `json:"subject"`
		Title   string `json:"title"`
		Source  string `json:"source"`
	}
	_ = readJSON(r, &body)
	if body.Source == "" {
		body.Source = "manual"
	}
	if body.Subject == "" {
		if st := s.ci.State(); st.Current != nil {
			body.Subject = st.Current.Subject
		}
	}
	if err := s.engine.Start(body.Subject, body.Title, body.Source); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "session": s.engine.SessionCopy()})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Report bool `json:"report"`
	}
	_ = readJSON(r, &body)
	if body.Report {
		if err := s.engine.StopAndReport(); err != nil {
			writeErr(w, 400, err)
			return
		}
	} else {
		if err := s.engine.Stop(); err != nil {
			writeErr(w, 400, err)
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "session": s.engine.SessionCopy()})
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"sessionId"`
	}
	_ = readJSON(r, &body)
	if body.SessionID == "" {
		if err := s.engine.GenerateReport(s.engine.SessionCopy()); err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "session": s.engine.SessionCopy()})
		return
	}
	sess, err := s.store.Load(body.SessionID)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.engine.GenerateReport(sess); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "session": sess})
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.CaptureNow(); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question string `json:"question"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(body.Question) == "" {
		writeErr(w, 400, fmt.Errorf("问题不能为空"))
		return
	}
	ans, err := s.engine.Ask(body.Question)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "answer": ans})
}

func (s *Server) handleUtterance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.engine.AppendUtterance(body.Text); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"sessionId"`
		SegmentID string `json:"segmentId"`
		Notes     string `json:"notes"`
		Summary   string `json:"summary"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	sess := s.engine.Session()
	if body.SessionID != "" {
		if loaded, err := s.store.Load(body.SessionID); err == nil {
			sess = loaded
		}
	}
	if sess == nil {
		writeErr(w, 404, fmt.Errorf("会话不存在"))
		return
	}
	for _, seg := range sess.Segments {
		if seg.ID == body.SegmentID {
			if body.Notes != "" {
				seg.Notes = body.Notes
			}
			if body.Summary != "" {
				seg.Summary = body.Summary
			}
		}
	}
	if err := s.store.Save(sess); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.List()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"sessions": list})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		if cur := s.engine.SessionCopy(); cur != nil {
			writeJSON(w, map[string]any{"session": cur, "live": true})
			return
		}
		writeJSON(w, map[string]any{"session": nil})
		return
	}
	sess, err := s.store.Load(id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	live := false
	if cur := s.engine.SessionCopy(); cur != nil && cur.ID == id {
		sess = cur
		live = true
	}
	writeJSON(w, map[string]any{"session": sess, "live": live})
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/media/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	sid, name := parts[0], parts[1]
	if strings.ContainsAny(sid+name, `\`) || strings.Contains(name, "..") || strings.Contains(sid, "..") {
		http.Error(w, "bad path", 400)
		return
	}
	p := filepath.Join(s.paths.Captures, sid, name)
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, p)
}

func (s *Server) handleReportFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/report-file/")
	if strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) {
		http.Error(w, "bad path", 400)
		return
	}
	kind := r.URL.Query().Get("type")
	ext := ".html"
	if kind == "pdf" {
		ext = ".pdf"
	}
	p := filepath.Join(s.paths.Reports, id+ext)
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	if kind == "pdf" {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", id+".pdf"))
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	http.ServeFile(w, r, p)
}

func (s *Server) handleTimetable(w http.ResponseWriter, r *http.Request) {
	st := s.ci.State()
	writeJSON(w, map[string]any{"state": st, "today": st.Today})
}

// handleASRTest 用配置的转写后端处理一段上传的 WAV，用于验证语音链路。
func (s *Server) handleASRTest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, fmt.Errorf("请以 multipart 上传 wav 文件（字段名 file）"))
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, fmt.Errorf("缺少 file 字段"))
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 32<<20))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg := s.cfg.Get()
	tr := asr.New(cfg.ASR)
	text, err := tr.Transcribe(r.Context(), raw, cfg.ASR.SampleRate)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "backend": tr.Describe()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "text": text, "backend": tr.Describe()})
}

// handleNotifyTest 向已启用的渠道发一条测试消息。
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	s.notifier.Update(s.cfg.Get().Notify)
	writeJSON(w, map[string]any{"ok": true, "results": s.notifier.Test()})
}

// handleNotifyPresets 返回内置通知渠道模板。
func (s *Server) handleNotifyPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"presets": notify.Presets()})
}

// handleUpdateCheck 检查 GitHub 上是否有新版本。
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	g := s.cfg.Get().GitHub
	writeJSON(w, update.Check(g.Repo, g.Token))
}

// handleThemesList 列出内置与自定义主题。
func (s *Server) handleThemesList(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	writeJSON(w, map[string]any{"builtins": theme.Builtins(), "custom": cfg.UI.CustomThemes, "vars": theme.Vars})
}

// handleThemeSave 保存/覆盖自定义主题。
func (s *Server) handleThemeSave(w http.ResponseWriter, r *http.Request) {
	var t config.CustomTheme
	if err := readJSON(r, &t); err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg, err := s.cfg.Update(func(c *config.Config) error { return theme.Save(c, t) })
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "custom": cfg.UI.CustomThemes})
}

// handleThemeDelete 删除自定义主题。
func (s *Server) handleThemeDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil || body.ID == "" {
		writeErr(w, 400, fmt.Errorf("缺少 id"))
		return
	}
	cfg, _ := s.cfg.Update(func(c *config.Config) error {
		theme.Delete(c, body.ID)
		return nil
	})
	writeJSON(w, map[string]any{"ok": true, "custom": cfg.UI.CustomThemes})
}

// handleThemeExport 导出主题 JSON（分享用）。
func (s *Server) handleThemeExport(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	t, ok := theme.Find(s.cfg.Get(), id)
	if !ok {
		writeErr(w, 404, fmt.Errorf("找不到主题 %s", id))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=classnote-theme-"+id+".json")
	_, _ = w.Write(theme.ExportJSON(t))
}

// handleThemeImport 导入主题（市场或本地文件）。
func (s *Server) handleThemeImport(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	t, err := theme.ImportJSON(raw)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg, err := s.cfg.Update(func(c *config.Config) error { return theme.Save(c, t) })
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "theme": t, "custom": cfg.UI.CustomThemes})
}

// handleThemeMarket 拉取主题市场清单。
func (s *Server) handleThemeMarket(w http.ResponseWriter, r *http.Request) {
	items, err := theme.Market(r.URL.Query().Get("url"))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "themes": items})
}

// handlePluginsList 列出技能包。
func (s *Server) handlePluginsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"skills": s.plugins.List(), "dir": s.plugins.Dir()})
}

// handlePluginSave 保存技能包。
func (s *Server) handlePluginSave(w http.ResponseWriter, r *http.Request) {
	var sk plugin.Skill
	if err := readJSON(r, &sk); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.plugins.Save(sk); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "skills": s.plugins.List()})
}

// handlePluginToggle 开关技能包。
func (s *Server) handlePluginToggle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil || body.ID == "" {
		writeErr(w, 400, fmt.Errorf("缺少 id"))
		return
	}
	if !s.plugins.SetEnabled(body.ID, body.Enabled) {
		writeErr(w, 404, fmt.Errorf("找不到技能包 %s", body.ID))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "skills": s.plugins.List()})
}

// handlePluginDelete 删除技能包。
func (s *Server) handlePluginDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil || body.ID == "" {
		writeErr(w, 400, fmt.Errorf("缺少 id"))
		return
	}
	if err := s.plugins.Delete(body.ID); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "skills": s.plugins.List()})
}

// handleAgentInfo 返回 Agent 权限与可用工具。
func (s *Server) handleAgentInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	writeJSON(w, map[string]any{
		"permission": cfg.Agent.Permission,
		"model":      cfg.Agent.Model,
		"tools":      s.plugins.Tools(),
		"skills":     len(s.plugins.Enabled()),
		"levels": []map[string]string{
			{"id": "all", "name": "全部访问", "note": "Agent 可直接使用全部本地能力，不再询问"},
			{"id": "remind", "name": "敏感操作提醒", "note": "涉及写文件/执行命令时先提示（推荐）"},
			{"id": "review", "name": "全部人工审查", "note": "每一步操作都要人工确认"},
		},
	})
}

// handleAgentPermission 修改 Agent 权限级别。
func (s *Server) handleAgentPermission(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Permission string `json:"permission"`
		Model      string `json:"model"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Permission != "" && body.Permission != "all" && body.Permission != "remind" && body.Permission != "review" {
		writeErr(w, 400, fmt.Errorf("权限级别只能是 all / remind / review"))
		return
	}
	cfg, err := s.cfg.Update(func(c *config.Config) error {
		if body.Permission != "" {
			c.Agent.Permission = body.Permission
		}
		if body.Model != "" {
			c.Agent.Model = body.Model
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "permission": cfg.Agent.Permission, "model": cfg.Agent.Model})
}

func (s *Server) handleMicTest(w http.ResponseWriter, r *http.Request) {
	n := audio.Devices()
	if n <= 0 {
		writeJSON(w, map[string]any{"ok": false, "error": "未检测到录音设备"})
		return
	}
	rec := audio.New(16000)
	if err := rec.Start(-1); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var peak float64
	deadline := time.After(2500 * time.Millisecond)
loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-rec.Output():
			if p := rec.Peak(); p > peak {
				peak = p
			}
		}
	}
	rec.Stop()
	writeJSON(w, map[string]any{"ok": true, "peak": peak, "devices": n,
		"message": fmt.Sprintf("已采集 2.5 秒，峰值 %.3f", peak)})
}

func (s *Server) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"classIsland": s.ci.State(),
		"icc":         s.icc.State(),
		"processes": map[string]any{
			"classisland": procOf("ClassIsland.exe", "ClassIsland.Desktop.exe"),
			"wps":         procOf("wpp.exe", "wps.exe"),
			"powerpoint":  procOf("POWERPNT.EXE"),
		},
	})
}

func (s *Server) handleICCInject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DLL string `json:"dll"`
	}
	_ = readJSON(r, &body)
	src := body.DLL
	if src == "" {
		exeDir, _ := os.Executable()
		src = integration.FindLocalPlugin(filepath.Dir(exeDir))
	}
	st, err := s.icc.CopyPlugin(src)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "state": st})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "state": st,
		"message": "已注入。若 ICC 正在运行，请重启它以加载插件。"})
}

func (s *Server) handleICCEject(w http.ResponseWriter, r *http.Request) {
	st, err := s.icc.RemovePlugin()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "state": st})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "state": st})
}

func (s *Server) handleICCEvent(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	name, _ := body["event"].(string)
	if name == "" {
		name = "unknown"
	}
	s.icc.Event(name, body)
	s.broadcast(lesson.Event{Type: "icc", Data: body, At: time.Now()})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleCIPush(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subject string `json:"subject"`
		InClass bool   `json:"inClass"`
		Start   string `json:"start"`
		End     string `json:"end"`
		Room    string `json:"room"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	now := time.Now()
	st := integration.ClassState{Detected: true, InClass: body.InClass,
		Source: "push", UpdatedAt: now}
	if body.Subject != "" {
		l := integration.Lesson{Subject: body.Subject, Room: body.Room}
		if t, err := time.ParseInLocation("15:04", body.Start, now.Location()); err == nil {
			l.Start = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		}
		if t, err := time.ParseInLocation("15:04", body.End, now.Location()); err == nil {
			l.End = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		}
		st.Current = &l
	}
	s.ci.Push(st)
	s.broadcast(lesson.Event{Type: "classisland", Data: st, At: now})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleFileDialog(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")
	if title == "" {
		title = "选择文件"
	}
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "可执行文件 (*.exe)|*.exe|所有文件 (*.*)|*.*"
	}
	dir := r.URL.Query().Get("dir")
	if dir != "" {
		dir = filepath.Clean(dir)
	}
	ps := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms;
$d = New-Object System.Windows.Forms.OpenFileDialog;
$d.Title = %q;
$d.Filter = %q;
if (%q -ne '') { $d.InitialDirectory = %q }
if ($d.ShowDialog() -eq 'OK') { Write-Output $d.FileName }`,
		title, filter, dir, dir)
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-Command", ps)
	out, err := cmd.Output()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	path := strings.TrimSpace(string(out))
	writeJSON(w, map[string]any{"ok": path != "", "path": path})
}

func procOf(names ...string) map[string]any {
	p, ok := capture.FindProcessInfo(names...)
	return map[string]any{"running": ok, "name": p.Name, "path": p.Path, "pid": p.PID}
}

// handleOpen 用资源管理器打开数据目录，便于用户查看截图与报告。
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	_ = readJSON(r, &body)
	var dir string
	switch body.Target {
	case "data":
		dir = filepath.Join(s.paths.Root, "data")
	case "reports":
		dir = s.paths.Reports
	case "logs":
		dir = s.paths.Logs
	case "captures":
		dir = s.paths.Captures
	default:
		dir = s.paths.Root
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, 500, err)
		return
	}
	cmd := exec.Command("explorer.exe", dir)
	_ = cmd.Start()
	go func() { _ = cmd.Wait() }()
	writeJSON(w, map[string]any{"ok": true, "dir": dir})
}

func (s *Server) handleModelsAlias(w http.ResponseWriter, r *http.Request) {
	s.handleModels(w, r)
}

var _ = strconv.Itoa
var _ = logx.Infof
var _ = store.Meta{}
