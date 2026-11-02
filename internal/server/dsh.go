package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"classnote/internal/dsh"
	"classnote/internal/lesson"
)

// dshRuntime 惰性创建 DSH 运行时（进程内单例）。
func (s *Server) dshRuntime() *dsh.Runtime {
	s.dshOnce.Do(func() {
		s.dsh = dsh.New(filepath.Join(s.paths.Root, "data", "dsh"), func(line string) {
			s.broadcastDSH(line, "log")
		})
	})
	return s.dsh
}

// handleDSHStatus 返回运行时状态。
func (s *Server) handleDSHStatus(w http.ResponseWriter, r *http.Request) {
	rt := s.dshRuntime()
	st := rt.Status()
	writeJSON(w, st)
}

// handleDSHInstall 在后台装运行时。
//
// 这里必须异步：装一次要下几百兆、跑几分钟，HTTP 请求早超时了。
// 进度通过 SSE 的 dsh_log 事件推给页面，页面上是滚动的安装日志。
func (s *Server) handleDSHInstall(w http.ResponseWriter, r *http.Request) {
	rt := s.dshRuntime()
	if rt.Status().Busy {
		writeErr(w, 429, fmt.Errorf("已经有一个安装任务在跑"))
		return
	}
	go func() {
		// 装完可能十几分钟，给足时间；ctx 只是兜底，避免永久挂住。
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := rt.Install(ctx); err != nil {
			s.broadcastDSH("安装失败："+err.Error(), "err")
			return
		}
		s.broadcastDSH("运行时已就绪，正在启动", "ok")
		if err := rt.Start(ctx); err != nil {
			s.broadcastDSH("启动失败："+err.Error(), "err")
			return
		}
	}()
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// handleDSHStart 启动运行时。
func (s *Server) handleDSHStart(w http.ResponseWriter, r *http.Request) {
	rt := s.dshRuntime()
	if rt.Running() {
		writeJSON(w, map[string]any{"ok": true, "url": rt.URL()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "url": rt.URL()})
}

// handleDSHStop 停止运行时。
func (s *Server) handleDSHStop(w http.ResponseWriter, r *http.Request) {
	rt := s.dshRuntime()
	rt.Stop()
	writeJSON(w, map[string]any{"ok": true})
}

// handleDSHPlugin 安装或卸载一个 DSH 插件。
//
// 这是「DSH 渠道」真正的落地：之前只能复制一条命令让用户自己去 DSH 里敲，
// 现在 ClassNote 直接把它装进自己的运行时。
func (s *Server) handleDSHPlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Package string `json:"package"`
		Action  string `json:"action"`
		Force   bool   `json:"force"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	pkg := strings.TrimSpace(body.Package)
	if pkg == "" {
		writeErr(w, 400, fmt.Errorf("缺少 package"))
		return
	}
	if len(pkg) > 214 {
		// npm 包名上限 214 字符，超了肯定不是正常输入。
		writeErr(w, 400, fmt.Errorf("包名太长"))
		return
	}
	rt := s.dshRuntime()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var err error
	switch body.Action {
	case "remove":
		err = rt.RemovePlugin(ctx, pkg)
	default:
		err = rt.AddPlugin(ctx, pkg, body.Force)
	}
	if err != nil {
		// 版本不兼容单独回 409：界面据此弹「仍然安装」，
		// 而不是把一段英文栈甩给老师。
		var ie *dsh.IncompatibleError
		if errors.As(err, &ie) {
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, map[string]any{
				"error":  ie.Error(),
				"code":   "incompatible",
				"pkg":    ie.Pkg,
				"want":   ie.Want,
				"have":   ie.Have,
				"detail": "这个插件要求的 DSH 版本与当前运行时不一致。仍然安装可能崩溃或丢数据。",
			})
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "plugins": rt.Plugins()})
}

// broadcastDSH 推一条安装日志给页面。
func (s *Server) broadcastDSH(line, level string) {
	s.broadcast(lesson.Event{
		Type: "dsh_log",
		Data: map[string]any{"line": line, "level": level},
		At:   time.Now(),
	})
}

// Shutdown 停掉后台子进程。
//
// 程序退出时必须调用：DSH 是独立进程，不跟着 ClassNote 一起消失的话，
// 下次启动会发现端口被占、或者机器上莫名多出一个 node。
func (s *Server) Shutdown() {
	// 这里不能碰 dshOnce：Do 一旦执行过，之后 dshRuntime() 就再也建不出来。
	if s.dsh != nil {
		s.dsh.Stop()
	}
}
