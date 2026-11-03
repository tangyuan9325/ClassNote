package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"classnote/internal/asr"
	"classnote/internal/config"
	"classnote/internal/lesson"
	"classnote/internal/logx"
)

// asrModels 惰性创建模型管理器。
//
// 惰性是为了不拖慢启动：管理器的构造要读目录、可能扫 .part 文件，
// 而这些只在打开设置页时才用得上。
func (s *Server) asrModels() *asr.ModelManager {
	s.asrOnce.Do(func() {
		var lastEmit time.Time
		var mu sync.Mutex
		s.asrMgr = asr.NewManager(func(p asr.Progress) {
			// 下载进度本身已按 400ms 节流，这里只挡住「同样的数字重复推」。
			mu.Lock()
			if time.Since(lastEmit) < 250*time.Millisecond &&
				p.State != "done" && p.State != "error" && p.State != "canceled" {
				mu.Unlock()
				return
			}
			lastEmit = time.Now()
			mu.Unlock()
			s.broadcast(lesson.Event{Type: "asr_model", Data: p, At: time.Now()})
		})
		asr.OnModelReady(func(m asr.Model) { s.activateModel(m) })
	})
	return s.asrMgr
}

// BootstrapASR 在启动时准备语音模型。
//
// 三种情况：
//  1. 有模型 → 直接起 whisper-server（老行为，桌面版随包带模型）；
//  2. 没模型、但随包 whisper 运行时在 → 后台下载默认轻量模型，
//     不阻塞启动，同步进度到界面；
//  3. 没运行时（便携包被裁掉了）→ 什么都不做，设置页会说明。
func (s *Server) BootstrapASR() {
	dir := asr.RuntimeDir()
	if dir == "" {
		return
	}
	cfg := s.cfg.Get()
	models := asr.LocalModels(dir)
	if len(models) > 0 {
		model := cfg.ASR.LocalModel
		if model == "" {
			model = models[0]
		}
		lang := cfg.ASR.Language
		port := cfg.ASR.LocalPort
		if port == 0 {
			port = 18095
		}
		if _, err := asr.StartLocalLang(dir, model, port, lang); err != nil {
			logx.Warnf("asr", "本地语音模型未能启动（%v）", err)
			return
		}
		logx.Infof("asr", "本地语音模型已就绪（%s）", filepath.Base(model))
		_, _ = s.cfg.Update(func(c *config.Config) error {
			c.ASR.Mode = "local"
			c.ASR.LocalModel = model
			c.ASR.LocalPort = port
			return nil
		})
		s.engine.Update(s.cfg.Get())
		return
	}
	// 没模型：只有老师真的配了云端 Key 才不下载。
	// BaseURL 在默认配置里是预填的示例地址，不能当作「他想用云端」的判据，
	// 否则默认配置（mode=local）也会被这条挡掉，语音永远准备不好。
	if cfg.ASR.Mode == "api" && strings.TrimSpace(cfg.ASR.APIKey) != "" {
		return
	}
	if cfg.ASR.Mode == "off" {
		return
	}
	mgr := s.asrModels()
	go func() {
		// 等前端连上 SSE 再开始，否则进度全推给了空气。
		time.Sleep(6 * time.Second)
		if mgr.Busy() {
			return
		}
		// 已经上课了就别下：60 MB 会抢掉网页版 AI 的带宽，
		// 而老师此刻需要的是能用的转写，不是一个更准的模型。
		if s.engine.Status().Running {
			logx.Infof("asr", "正在上课，跳过语音模型的自动下载")
			return
		}
		if has := anyModelInstalled(); has != "" {
			return
		}
		m, _ := asr.FindModel(asr.DefaultModelID)
		logx.Infof("asr", "首次启动，开始后台下载语音模型 %s（%.1f MB）", m.Name, float64(m.Size)/(1<<20))
		if err := mgr.Start(asr.DefaultModelID, cfg.ASR.Mirror); err != nil {
			logx.Warnf("asr", "自动下载语音模型失败：%v", err)
		}
	}()
}

// anyModelInstalled 返回第一个已装模型的文件名，没有则返回空。
func anyModelInstalled() string {
	for _, m := range asr.Models {
		if asr.HasModel(m) {
			return m.File
		}
	}
	return ""
}

// activateModel 把某个模型设为当前使用，并重启服务。
//
// force=false 表示是「下载完成后自动启用」：这种情况下如果正在上课，
// 就只写配置不动进程——重启 whisper 会打断正在进行的转写，
// 老师讲着讲着字幕断了比「晚一节课生效」难解释得多。
func (s *Server) activateModel(m asr.Model) {
	s.activateModelForce(m, false)
}

func (s *Server) activateModelForce(m asr.Model, force bool) {
	dir := asr.RuntimeDir()
	if dir == "" {
		return
	}
	cfg := s.cfg.Get()
	port := cfg.ASR.LocalPort
	if port == 0 {
		port = 18095
	}
	path := asr.LocalModelPath(m)

	if !force && s.engine.Status().Running {
		if _, err := s.cfg.Update(func(c *config.Config) error {
			c.ASR.Mode = "local"
			c.ASR.LocalModel = path
			return nil
		}); err == nil {
			s.engine.Update(s.cfg.Get())
		}
		logx.Infof("asr", "正在上课，%s 已就绪，下堂课自动启用（不打断当前转写）", m.Name)
		s.broadcast(lesson.Event{
			Type: "asr_model",
			Data: asr.Progress{ID: m.ID, State: "done",
				Error: "正在上课，模型已保存，下堂课自动启用"},
			At: time.Now(),
		})
		return
	}
	srv, err := asr.RestartLocal(dir, path, port, cfg.ASR.Language)
	if err != nil {
		logx.Warnf("asr", "启用模型 %s 失败：%v", m.Name, err)
		s.broadcast(lesson.Event{
			Type: "asr_model",
			Data: asr.Progress{ID: m.ID, State: "error", Error: err.Error()},
			At:   time.Now(),
		})
		return
	}
	_, _ = s.cfg.Update(func(c *config.Config) error {
		c.ASR.Mode = "local"
		c.ASR.LocalModel = path
		c.ASR.LocalPort = srv.Port()
		return nil
	})
	s.engine.Update(s.cfg.Get())
	logx.Infof("asr", "已切换到 %s（端口 %d）", m.Name, srv.Port())
	// 这里不再广播 done：下载流程的 finish() 已经发过一次，
	// 再发一次前端会弹两遍「已就绪」。
}

// handleASRModels 返回模型清单与本地状态。
func (s *Server) handleASRModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	mgr := s.asrModels()
	port := cfg.ASR.LocalPort
	if port == 0 {
		port = 18095
	}
	st := mgr.Snap(port, cfg.ASR.LocalModel, asr.LocalHealth(port))
	writeJSON(w, map[string]any{
		"ok":       true,
		"dir":      st.Dir,
		"ready":    st.Ready,
		"port":     st.Port,
		"active":   st.Active,
		"models":   st.Models,
		"task":     st.Task,
		"platform": st.Platform,
		"sources":  st.Sources,
		"default":  asr.DefaultModelID,
		"mirror":   cfg.ASR.Mirror,
		"mode":     cfg.ASR.Mode,
	})
}

// handleASRDownload 下载指定模型。
func (s *Server) handleASRDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		Mirror string `json:"mirror"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" || len(id) > 64 {
		writeErr(w, 400, fmt.Errorf("模型 ID 不合法"))
		return
	}
	m, ok := asr.FindModel(id)
	if !ok {
		writeErr(w, 400, fmt.Errorf("未知的模型：%s", id))
		return
	}
	mirror := strings.TrimSpace(body.Mirror)
	if mirror != "" && !strings.HasPrefix(mirror, "https://") {
		writeErr(w, 400, fmt.Errorf("下载源必须是 https 地址"))
		return
	}
	if err := s.asrModels().Start(id, mirror); err != nil {
		writeErr(w, 409, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "size": m.Size})
}

// handleASRCancel 取消下载。
func (s *Server) handleASRCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	_ = readJSON(r, &body)
	s.asrModels().Cancel(strings.TrimSpace(body.ID))
	writeJSON(w, map[string]any{"ok": true})
}

// handleASRDelete 删除已下载的模型，释放磁盘。
func (s *Server) handleASRDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	m, ok := asr.FindModel(strings.TrimSpace(body.ID))
	if !ok {
		writeErr(w, 400, fmt.Errorf("未知的模型"))
		return
	}
	cfg := s.cfg.Get()
	// 正在用的那个不能删：删了进程还抱着文件，重启就找不到模型了。
	if filepath.Base(cfg.ASR.LocalModel) == m.File {
		writeErr(w, 409, fmt.Errorf("这是当前正在使用的模型，请先切换到别的模型再删除"))
		return
	}
	path := asr.LocalModelPath(m)
	if path == "" {
		writeErr(w, 400, fmt.Errorf("模型目录不可用"))
		return
	}
	if err := os.Remove(path); err != nil {
		writeErr(w, 500, fmt.Errorf("删除失败：%w", err))
		return
	}
	_ = os.Remove(path + ".part")
	writeJSON(w, map[string]any{"ok": true})
}

// handleASRActivate 切换当前使用的模型。
func (s *Server) handleASRActivate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	m, ok := asr.FindModel(strings.TrimSpace(body.ID))
	if !ok {
		writeErr(w, 400, fmt.Errorf("未知的模型"))
		return
	}
	if !asr.HasModel(m) {
		writeErr(w, 400, fmt.Errorf("%s 还没下载", m.Name))
		return
	}
	// 手动点的「使用」是明确意图，即使在上课也照做。
	go s.activateModelForce(m, true)
	writeJSON(w, map[string]any{"ok": true, "id": m.ID, "restarting": true})
}

// handleASRStatus 是给顶部状态条用的轻量接口。
func (s *Server) handleASRStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	port := cfg.ASR.LocalPort
	if port == 0 {
		port = 18095
	}
	mgr := s.asrModels()
	task := mgr.Current()
	active := ""
	if m, ok := asr.FindModelByFile(filepath.Base(cfg.ASR.LocalModel)); ok {
		active = m.ID
	}
	writeJSON(w, map[string]any{
		"ok":        true,
		"ready":     asr.LocalHealth(port),
		"port":      port,
		"active":    active,
		"mode":      cfg.ASR.Mode,
		"task":      task,
		"installed": anyModelInstalled() != "",
	})
}

// ASRShutdown 退出前停掉语音服务，别把 whisper-server 留成孤儿。
func (s *Server) ASRShutdown() {
	if srv := asr.RunningServer(); srv != nil {
		srv.Stop()
	}
}
