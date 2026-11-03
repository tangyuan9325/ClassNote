// Package server 提供本地 HTTP 服务：静态界面、REST API 与 SSE 事件流。
package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"classnote/internal/agent"
	"classnote/internal/appinfo"
	"classnote/internal/asr"
	"classnote/internal/config"
	"classnote/internal/dsh"
	"classnote/internal/integration"
	"classnote/internal/lesson"
	"classnote/internal/llm"
	"classnote/internal/logx"
	"classnote/internal/notify"
	"classnote/internal/paths"
	"classnote/internal/plugin"
	"classnote/internal/store"
	"classnote/internal/theme"
	"classnote/internal/update"
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
	plugins   *plugin.Registry
	ln        net.Listener // 当前监听器；切换局域网开关时需要重绑
	lnHost    string
	lnMu      sync.Mutex

	// 语音模型下载器（惰性：只在用到时读目录）
	asrOnce sync.Once
	asrMgr  *asr.ModelManager

	// DSH 运行时（按需安装，6 亿字节级别，懒加载）
	dshOnce sync.Once
	dsh     *dsh.Runtime

	// Agent 工具表与审批通道
	toolMu    sync.Mutex
	tools     *agent.Toolset
	approveMu sync.Mutex
	approvals map[string]chan bool
	agentBusy atomic.Bool

	// 更新包下载器（惰性：status 这类轻请求不该触发网络动作）
	updOnce sync.Once
	updDl   *update.Downloader
	// applying 保证同一时刻只有一个安装流程；装到一半再点一次会写坏目录
	applying atomic.Bool

	// 退出钩子：main 注入后由 handleShutdown / 更新安装调用，
	// 以便顺带停掉 whisper-server、DSH 与托盘图标
	exitMu sync.Mutex
	exitFn func()
}

// New 创建服务。
func New(paths paths.Paths, cfg *config.Store, st *store.Store, engine *lesson.Engine,
	ci *integration.ClassIslandProvider, icc *integration.ICCBridge, webFS fs.FS) *Server {
	s := &Server{
		paths: paths, cfg: cfg, store: st, engine: engine, ci: ci, icc: icc,
		webFS: webFS, subs: map[chan lesson.Event]bool{}, started: time.Now(),
		notifier: notify.New(cfg.Get().Notify),
	}
	theme.LoadBundled()
	plugins, err := plugin.Open(filepath.Join(paths.Root, "data", "plugins", "skills"))
	if err != nil {
		logx.Warnf("server", "技能包目录不可用: %v", err)
	}
	if plugins == nil {
		// 不留给调用方 nil：插件相关接口一律走空仓库，避免解引用直接 500。
		plugins = plugin.Empty()
	}
	s.plugins = plugins
	if plugins != nil {
		engine.SetSkillPrompt(plugins.Prompt())
	}
	s.mux = http.NewServeMux()
	s.routes()
	engine.OnEvent(func(e lesson.Event) { s.broadcast(e) })
	return s
}

// syncPluginPrompt 把启用插件的提示词同步给分析引擎。
//
// 之前只在启动时设一次：装完插件不重启就不生效，老师会以为插件坏了。
func (s *Server) syncPluginPrompt() {
	if s.plugins == nil || s.engine == nil {
		return
	}
	s.engine.SetSkillPrompt(s.plugins.Prompt())
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
	// 每门课一条固定对话的记录文件：放在数据目录里，跟着安装走。
	cfg.WebAI.ChatsFile = filepath.Join(s.paths.Root, "data", "webai-chats.json")
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
	m.HandleFunc("/api/webai/chats", s.handleWebChats)
	m.HandleFunc("/api/webai/chats/set", s.handleWebChatSet)
	m.HandleFunc("/api/webai/prime", s.handleWebChatPrime)
	m.HandleFunc("/api/ci/subjects", s.handleCISubjects)
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
	m.HandleFunc("/api/asr/models", s.handleASRModels)
	m.HandleFunc("/api/asr/models/download", s.handleASRDownload)
	m.HandleFunc("/api/asr/models/cancel", s.handleASRCancel)
	m.HandleFunc("/api/asr/models/delete", s.handleASRDelete)
	m.HandleFunc("/api/asr/models/activate", s.handleASRActivate)
	m.HandleFunc("/api/asr/status", s.handleASRStatus)
	m.HandleFunc("/api/asr/test", s.handleASRTest)
	m.HandleFunc("/api/notify/test", s.handleNotifyTest)
	m.HandleFunc("/api/notify/presets", s.handleNotifyPresets)
	m.HandleFunc("/api/update/check", s.handleUpdateCheck)
	m.HandleFunc("/api/update/status", s.handleUpdateStatus)
	m.HandleFunc("/api/update/nodes", s.handleUpdateNodes)
	m.HandleFunc("/api/update/download", s.handleUpdateDownload)
	m.HandleFunc("/api/update/cancel", s.handleUpdateCancel)
	m.HandleFunc("/api/update/apply", s.handleUpdateApply)
	m.HandleFunc("/api/themes", s.handleThemesList)
	m.HandleFunc("/api/themes/save", s.handleThemeSave)
	m.HandleFunc("/api/themes/delete", s.handleThemeDelete)
	m.HandleFunc("/api/themes/export", s.handleThemeExport)
	m.HandleFunc("/api/themes/import", s.handleThemeImport)
	m.HandleFunc("/api/themes/market", s.handleThemeMarket)
	m.HandleFunc("/api/plugins", s.handlePluginsList)
	m.HandleFunc("/api/plugins/save", s.handlePluginSave)
	m.HandleFunc("/api/plugins/toggle", s.handlePluginToggle)
	m.HandleFunc("/api/plugins/delete", s.handlePluginDelete)
	m.HandleFunc("/api/plugins/market", s.handlePluginMarket)
	m.HandleFunc("/api/plugins/install", s.handlePluginInstall)
	m.HandleFunc("/api/plugins/uninstall", s.handlePluginUninstall)
	m.HandleFunc("/api/plugins/slots", s.handlePluginSlots)
	m.HandleFunc("/api/dsh/status", s.handleDSHStatus)
	m.HandleFunc("/api/dsh/install", s.handleDSHInstall)
	m.HandleFunc("/api/dsh/start", s.handleDSHStart)
	m.HandleFunc("/api/dsh/stop", s.handleDSHStop)
	m.HandleFunc("/api/dsh/plugin", s.handleDSHPlugin)
	m.HandleFunc("/api/agent/info", s.handleAgentInfo)
	m.HandleFunc("/api/agent/run", s.handleAgentRun)
	m.HandleFunc("/api/agent/chat", s.handleAgentRun) // v1.2.6 的旧地址，行为保持不变（无工具时等价纯问答）
	m.HandleFunc("/api/agent/approve", s.handleAgentApprove)
	m.HandleFunc("/api/agent/permission", s.handleAgentPermission)
	m.HandleFunc("/api/integrations/status", s.handleIntegrations)
	m.HandleFunc("/api/integrations/icc/inject", s.handleICCInject)
	m.HandleFunc("/api/integrations/icc/eject", s.handleICCEject)
	m.HandleFunc("/api/integrations/icc/event", s.handleICCEvent)
	m.HandleFunc("/api/integrations/classisland/state", s.handleCIPush)
	m.HandleFunc("/api/integrations/classisland/plugin", s.handleCIPlugin)
	m.HandleFunc("/api/integrations/classisland/install", s.handleCIPluginInstall)
	m.HandleFunc("/api/integrations/classisland/uninstall", s.handleCIPluginUninstall)
	m.HandleFunc("/api/integrations/classisland/notify", s.handleCIPluginNotify)
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
	return s.listenOn(s.listenHost(), preferred)
}

// listenHost 决定绑定地址：开放局域网时监听所有网卡，否则只听本机。
func (s *Server) listenHost() string {
	if s.cfg.Get().Server.Access == "lan" {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// Restart 立刻按当前配置重绑端口，切换“允许局域网”不必重启程序。
func (s *Server) Restart() error {
	pref := s.port
	if pref == 0 {
		pref = 41735
	}
	_, err := s.listenOn(s.listenHost(), pref)
	return err
}

func (s *Server) listenOn(host string, preferred int) (int, error) {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if s.ln != nil {
		_ = s.ln.Close() // 只关监听器，已建立的连接不受影响
		s.ln = nil
	}
	for p := preferred; p < preferred+40; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, p))
		if err != nil {
			continue
		}
		s.port = p
		s.lnHost = host
		s.ln = ln
		go func(ln net.Listener) {
			srv := &http.Server{
				Handler:           s.csrfGuard(s.authMiddleware(s.mux)),
				ReadHeaderTimeout: 10 * time.Second,
			}
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				logx.Errorf("server", "HTTP 服务退出: %v", err)
			}
		}(ln)
		logx.Infof("server", "本地服务已启动 http://%s", ln.Addr().String())
		return p, nil
	}
	return 0, fmt.Errorf("找不到可用端口")
}

// authMiddleware 保护局域网访问：本机回环地址直接放行，其他来源必须携带访问密钥。
// 密钥可以放在 X-ClassNote-Token 头、?token= 查询参数或 cn_token Cookie 里；
// 用 ?token= 打开页面后会写入 Cookie，后续静态资源与 SSE 都能自动带上。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(s.cfg.Get().Server.Token)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("X-ClassNote-Token")
		fromQuery := false
		if got == "" {
			got = r.URL.Query().Get("token")
			fromQuery = got != ""
		}
		if got == "" {
			if c, err := r.Cookie("cn_token"); err == nil {
				got = c.Value
			}
		}
		if !secureEqual(got, token) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeErr(w, http.StatusUnauthorized, fmt.Errorf("访问密钥无效，请使用带 token 的链接"))
			} else {
				http.Error(w, "访问密钥无效，请使用带 token 的链接", http.StatusUnauthorized)
			}
			return
		}
		if fromQuery {
			http.SetCookie(w, &http.Cookie{
				Name: "cn_token", Value: token, Path: "/",
				MaxAge: 30 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
		}
		next.ServeHTTP(w, r)
	})
}

// csrfGuard 拦截跨站写请求。
//
// authMiddleware 对回环地址是直接放行的（本机用文件管理器打开就能用，
// 不要求先输密钥）。代价是：本机浏览器里任意一个恶意页面都能向
// 127.0.0.1:41735 发一个表单 POST，把 /api/shutdown、/api/lesson/start、
// /api/config 打成"用户自己的操作"——服务端看到的是来自 127.0.0.1 的请求。
//
// 浏览器发跨站写请求一定会带 Origin 或 Sec-Fetch-Site，而 curl、自检工具、
// 托盘菜单这些本机客户端不会带，正好用这一点把两者分开。
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			writeErr(w, http.StatusForbidden, fmt.Errorf("拒绝跨站请求"))
			return
		}
		// 没带 Origin 的本机客户端（curl / 自检 / 托盘）放行；
		// 带了的必须是本服务自己的源。
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !s.sameOrigin(origin) {
			writeErr(w, http.StatusForbidden, fmt.Errorf("拒绝跨站请求"))
			return
		}
		// 这两类是无须预检的简单请求，本程序没有任何接口用它们提交数据，
		// 直接挡掉可以再省一层。
		ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = strings.TrimSpace(ct[:i])
		}
		if ct == "text/plain" || ct == "application/x-www-form-urlencoded" {
			writeErr(w, http.StatusUnsupportedMediaType, fmt.Errorf("请使用 application/json 提交"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin 判断 Origin 是否指向本服务自己。
//
// 端口对上就认为是自己：别的站点要伪造，就得先占住 41735 这个端口，
// 而我们正监听着它。主机名再限定为回环或当前监听地址。
func (s *Server) sameOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if port != strconv.Itoa(s.port) {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || isLoopback(host+":0") {
		return true
	}
	s.lnMu.Lock()
	listenHost := s.lnHost
	s.lnMu.Unlock()
	return host == listenHost || listenHost == "" || listenHost == "0.0.0.0"
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Port 返回监听端口。
func (s *Server) Port() int { return s.port }

// URL 返回本地地址。
func (s *Server) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

// WriteRuntime 写入运行时信息，便于外部脚本对接。
func (s *Server) WriteRuntime() {
	info := map[string]any{
		"port": s.port, "url": s.URL(), "pid": os.Getpid(),
		"startedAt": s.started, "version": appinfo.Version,
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
	s.requestExit(300 * time.Millisecond)
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
