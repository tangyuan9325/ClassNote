// Package server 提供本地 HTTP 服务：静态界面、REST API 与 SSE 事件流。
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"classnote/internal/app"
	"classnote/internal/config"
	"classnote/internal/integration"
	"classnote/internal/lesson"
	"classnote/internal/llm"
	"classnote/internal/logx"
	"classnote/internal/notify"
	"classnote/internal/paths"
	"classnote/internal/store"
)

// Server 是本地应用服务。
type Server struct {
	paths     paths.Paths
	cfg       *config.Store
	store     *store.Store
	engine    *lesson.Engine
	ci        *integration.ClassIslandProvider
	icc       *integration.ICCBridge
	webFS     fs.FS
	port      int
	mux       *http.ServeMux
	subs      map[chan lesson.Event]bool
	subsMu    sync.Mutex
	clientMu  sync.Mutex
	client    llm.Client
	clientErr error
	started   time.Time
	notifier  *notify.Notifier
}

// New 创建服务。
func New(paths paths.Paths, cfg *config.Store, st *store.Store, engine *lesson.Engine,
	ci *integration.ClassIslandProvider, icc *integration.ICCBridge, webFS fs.FS) *Server {
	s := &Server{
		paths: paths, cfg: cfg, store: st, engine: engine, ci: ci, icc: icc,
		webFS: webFS, subs: map[chan lesson.Event]bool{}, started: time.Now(),
		notifier: notify.New(cfg.Get().Notify),
	}
	s.mux = http.NewServeMux()
	s.routes()
	engine.OnEvent(func(e lesson.Event) { s.broadcast(e) })
	return s
}

// Client 返回当前模型客户端（带缓存）。
func (s *Server) Client() llm.Client {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.client
}

// RefreshClient 根据配置重建模型客户端。
func (s *Server) RefreshClient() (llm.Client, error) {
	cfg := s.cfg.Get()
	// 网页版必须使用固定的用户数据目录，才能复用登录态。
	if cfg.WebAI.ProfileDir == "" {
		cfg.WebAI.ProfileDir = s.paths.WebCache
	}
	c, err := llm.New(cfg)
	s.clientMu.Lock()
	s.client = c
	s.clientErr = err
	s.clientMu.Unlock()
	if err == nil {
		s.engine.SetClient(c)
	} else {
		s.engine.SetClient(nil)
	}
	return c, err
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("/", s.handleIndex)
	m.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(s.webFS))))

	m.HandleFunc("/api/bootstrap", s.handleBootstrap)
	m.HandleFunc("/api/config", s.handleConfig)
	m.HandleFunc("/api/config/reset", s.handleConfigReset)
	m.HandleFunc("/api/env", s.handleEnv)
	m.HandleFunc("/api/dialog/file", s.handleFileDialog)
	m.HandleFunc("/api/provider/presets", s.handlePresets)
	m.HandleFunc("/api/provider/test", s.handleProviderTest)
	m.HandleFunc("/api/provider/models", s.handleModels)
	m.HandleFunc("/api/webai/login", s.handleWebLogin)
	m.HandleFunc("/api/webai/check", s.handleWebCheck)
	m.HandleFunc("/api/status", s.handleStatus)
	m.HandleFunc("/api/events", s.handleSSE)
	m.HandleFunc("/api/lesson/start", s.handleStart)
	m.HandleFunc("/api/lesson/stop", s.handleStop)
	m.HandleFunc("/api/lesson/report", s.handleReport)
	m.HandleFunc("/api/lesson/capture", s.handleCapture)
	m.HandleFunc("/api/lesson/ask", s.handleAsk)
	m.HandleFunc("/api/lesson/note", s.handleNote)
	m.HandleFunc("/api/lesson/utterance", s.handleUtterance)
	m.HandleFunc("/api/sessions", s.handleSessions)
	m.HandleFunc("/api/session", s.handleSession)
	m.HandleFunc("/api/media/", s.handleMedia)
	m.HandleFunc("/api/report-file/", s.handleReportFile)
	m.HandleFunc("/api/logs", s.handleLogs)
	m.HandleFunc("/api/timetable", s.handleTimetable)
	m.HandleFunc("/api/asr/mic", s.handleMicTest)
	m.HandleFunc("/api/asr/test", s.handleASRTest)
	m.HandleFunc("/api/notify/test", s.handleNotifyTest)
	m.HandleFunc("/api/notify/presets", s.handleNotifyPresets)
	m.HandleFunc("/api/integrations/status", s.handleIntegrations)
	m.HandleFunc("/api/integrations/icc/inject", s.handleICCInject)
	m.HandleFunc("/api/integrations/icc/eject", s.handleICCEject)
	m.HandleFunc("/api/integrations/icc/event", s.handleICCEvent)
	m.HandleFunc("/api/integrations/classisland/state", s.handleCIPush)
	m.HandleFunc("/api/events/test", s.handleEventTest)
	m.HandleFunc("/api/shutdown", s.handleShutdown)
	m.HandleFunc("/api/open", s.handleOpen)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	raw, err := fs.ReadFile(s.webFS, "index.html")
	if err != nil {
		http.Error(w, "界面文件缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// broadcast 向所有 SSE 订阅者推送事件。
// notifierSend 按事件类型推送机器人通知。
func (s *Server) notifierSend(kind string, data any) {
	if s.notifier == nil {
		return
	}
	s.notifier.Update(s.cfg.Get().Notify)
	switch kind {
	case "session_ended":
		s.notifier.Send(notify.Event{Title: "课堂已结束", Text: "笔记与报告已保存，可在应用里查看。", Level: "info"})
	case "report":
		s.notifier.Send(notify.Event{Title: "课堂报告已生成", Text: "报告（含逐页笔记）已就绪。", Level: "info"})
	case "auto_start":
		subj := ""
		if m, ok := data.(map[string]any); ok {
			subj, _ = m["subject"].(string)
		}
		s.notifier.Send(notify.Event{Title: "检测到上课", Text: "科目：" + subj + "，已自动开始记录。", Level: "info"})
	case "error":
		msg := ""
		if m, ok := data.(map[string]any); ok {
			msg, _ = m["message"].(string)
		}
		s.notifier.Send(notify.Event{Title: "ClassNote 出错了", Text: msg, Level: "error"})
	}
}

// Notifier 返回通知器，供接口层使用。
func (s *Server) Notifier() *notify.Notifier { return s.notifier }

func (s *Server) broadcast(e lesson.Event) {
	s.notifierSend(e.Type, e.Data)
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan lesson.Event, 32)
	s.subsMu.Lock()
	s.subs[ch] = true
	s.subsMu.Unlock()
	defer func() {
		s.subsMu.Lock()
		delete(s.subs, ch)
		s.subsMu.Unlock()
	}()

	fmt.Fprintf(w, "event: hello\ndata: {\"ok\":true}\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		case e := <-ch:
			raw, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, raw)
			fl.Flush()
		}
	}
}

func (s *Server) handleEventTest(w http.ResponseWriter, r *http.Request) {
	s.broadcast(lesson.Event{Type: "test", Data: map[string]string{"msg": "hello"}, At: time.Now()})
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// Listen 在空闲端口启动服务，返回实际端口。
func (s *Server) Listen(preferred int) (int, error) {
	addr := ""
	// 开放局域网时监听所有网卡，否则只听本机。
	host := "127.0.0.1"
	if s.cfg.Get().Server.Access == "lan" {
		host = "0.0.0.0"
	}
	for p := preferred; p < preferred+40; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, p))
		if err != nil {
			continue
		}
		s.port = p
		addr = ln.Addr().String()
		go func() {
			srv := &http.Server{
				Handler:           s.mux,
				ReadHeaderTimeout: 10 * time.Second,
			}
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				logx.Errorf("server", "HTTP 服务退出: %v", err)
			}
		}()
		logx.Infof("server", "本地服务已启动 http://%s", addr)
		return p, nil
	}
	return 0, fmt.Errorf("找不到可用端口")
}

// Port 返回监听端口。
func (s *Server) Port() int { return s.port }

// URL 返回本地地址。
func (s *Server) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

// WriteRuntime 写入运行时信息，便于外部脚本对接。
func (s *Server) WriteRuntime() {
	info := map[string]any{
		"port": s.port, "url": s.URL(), "pid": os.Getpid(),
		"startedAt": s.started, "version": app.Version,
	}
	raw, _ := json.MarshalIndent(info, "", "  ")
	_ = os.WriteFile(s.paths.Runtime, raw, 0o644)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 500 {
		n = 200
	}
	writeJSON(w, map[string]any{"entries": logx.Recent(n)})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}()
}

func (s *Server) absReport(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.paths.Root, p)
}
