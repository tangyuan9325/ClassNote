package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"classnote/internal/config"
	"classnote/internal/lesson"
	"classnote/internal/logx"
	"classnote/internal/update"
)

// updateDownloader 惰性创建更新包下载器。
//
// 惰性是为了让 /api/update/status 这类轻请求不触发任何网络动作；
// 真正开始下载时才建对象、才接 SSE 广播。
func (s *Server) updateDownloader() *update.Downloader {
	s.updOnce.Do(func() {
		var mu sync.Mutex
		var lastEmit time.Time
		s.updDl = update.NewDownloader(func(p update.Progress) {
			// 进度本身已按 400ms 节流，这里再挡一次重复推送。
			mu.Lock()
			if p.State == "downloading" && time.Since(lastEmit) < 200*time.Millisecond {
				mu.Unlock()
				return
			}
			lastEmit = time.Now()
			mu.Unlock()
			s.broadcast(lesson.Event{Type: "update_progress", Data: p, At: time.Now()})
		})
	})
	return s.updDl
}

// pickNodes 决定这次下载用哪些节点、按什么顺序。
//
// 四种情况：
//  1. 配了 Token 或选了「只连 GitHub」→ 只用直连。凭据不能交给第三方；
//     部分学校的网络策略也不允许访问反代。
//  2. 用户锁定某个节点 → 不测速，锁定节点优先，其余留作回落。
//  3. 关掉了自动测速 → 按候选顺序，上次选中的提前。
//  4. 默认 → 并发测速，最快的排第一。
//
// force 表示「这次是用户主动点测速」，此时绕过 2/3 的短路，
// 但仍然遵守 1 的限制。
func (s *Server) pickNodes(ctx context.Context, rawURL, override string, force bool) []update.Node {
	g := s.cfg.Get().GitHub
	want := strings.TrimSpace(override)
	if want == "" {
		want = g.Mirror
	}
	if want == "off" || g.Token != "" {
		if force && rawURL != "" {
			return update.TestNodes(ctx, update.ProbeReq{URL: rawURL, DirectOnly: true, Force: true})
		}
		return []update.Node{{ID: "direct", Name: "直连 GitHub", Prefix: ""}}
	}
	if !force {
		if want != "" && want != "auto" {
			nodes := update.Prefer(update.Candidates(g.Mirrors), want)
			if len(nodes) > 0 && nodes[0].ID == want {
				return nodes
			}
			logx.Warnf("update", "配置里的节点 %q 不存在，回退为自动测速", want)
		}
		if g.SpeedTestOff {
			return update.Prefer(update.Candidates(g.Mirrors), g.LastNode)
		}
	}
	nodes := update.TestNodes(ctx, update.ProbeReq{URL: rawURL, Custom: g.Mirrors, Force: force})
	if want != "" && want != "auto" && nodeOK(nodes, want) {
		nodes = update.Prefer(nodes, want)
	} else if g.LastNode != "" && nodeOK(nodes, g.LastNode) {
		// 上次选中的节点还活着就继续用它：两次测速之间的正常波动
		// 不该让用户看到「节点又换了」。死了就老老实实按这次测速排。
		nodes = update.Prefer(nodes, g.LastNode)
	}
	// 只有「自动测速」选出来的第一名才值得记住：
	// 固定节点的选择在配置里，不该被这里覆盖。
	if len(nodes) > 0 && nodes[0].OK && (want == "" || want == "auto") {
		go s.rememberNode(nodes[0].ID)
	}
	return nodes
}

// nodeOK 判断某个节点在本次测速里是否可用。
func nodeOK(nodes []update.Node, id string) bool {
	for _, n := range nodes {
		if n.ID == id {
			return n.OK
		}
	}
	return false
}

// rememberNode 记住这次选中的节点，下次启动少测一次速。
func (s *Server) rememberNode(id string) {
	if id == "" || s.cfg.Get().GitHub.LastNode == id {
		return
	}
	_, _ = s.cfg.Update(func(c *config.Config) error {
		c.GitHub.LastNode = id
		return nil
	})
}

// handleUpdateCheck 检查 GitHub 上是否有新版本。
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	g := s.cfg.Get().GitHub
	info := update.Check(g.Repo, g.Token)
	// 顺手把节点表带上：界面在「发现新版本」时要立刻显示测速结果，
	// 再单独发一次请求等于让用户多看一眼空白。
	directOnly := g.Token != ""
	nodes, cached := update.Cached("", directOnly, g.Mirrors)
	if len(info.Assets) > 0 {
		nodes, cached = update.Cached(info.Assets[0].URL, directOnly, g.Mirrors)
	}
	// 响应同时给出「平铺字段」和「info 子对象」：
	// 平铺是为了兼容 v1.2.6 起的旧客户端与 cmd/verify（它们直接读顶层），
	// info 是给新界面用的（要同时拿节点表，不想多一次请求）。
	flat := map[string]any{}
	if b, err := json.Marshal(info); err == nil {
		_ = json.Unmarshal(b, &flat)
	}
	flat["info"] = info
	flat["nodes"] = nodes
	flat["cached"] = cached
	flat["mirror"] = g.Mirror
	flat["speedTestOff"] = g.SpeedTestOff
	flat["lastNode"] = g.LastNode
	flat["running"] = s.engine.Status().Running
	writeJSON(w, flat)
}

// handleUpdateStatus 返回下载任务、上次升级结果与节点配置。
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	dl := s.updateDownloader()
	g := s.cfg.Get().GitHub
	out := map[string]any{
		"task":         dl.Current(),
		"mirror":       g.Mirror,
		"mirrors":      g.Mirrors,
		"speedTestOff": g.SpeedTestOff,
		"lastNode":     g.LastNode,
		"dir":          update.Dir(s.paths.Root),
		"running":      s.engine.Status().Running,
		"platform":     runtime.GOOS,
	}
	if res, ok := update.LastResult(s.paths.Root); ok {
		out["last"] = res
		out["hasLast"] = true
	}
	writeJSON(w, out)
}

// handleUpdateNodes 返回节点列表（可带一次测速）。
func (s *Server) handleUpdateNodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL   string `json:"url"`
		Force bool   `json:"force"`
	}
	_ = readJSON(r, &body)
	g := s.cfg.Get().GitHub
	directOnly := g.Token != ""

	if !body.Force {
		nodes, cached := update.Cached(body.URL, directOnly, g.Mirrors)
		writeJSON(w, map[string]any{
			"nodes": nodes, "cached": cached,
			"mirror": g.Mirror, "speedTestOff": g.SpeedTestOff, "lastNode": g.LastNode,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	nodes := s.pickNodes(ctx, body.URL, "", true)
	writeJSON(w, map[string]any{
		"nodes": nodes, "cached": false,
		"mirror": g.Mirror, "speedTestOff": g.SpeedTestOff, "lastNode": g.LastNode,
	})
}

// handleUpdateDownload 启动后台下载，进度走 SSE 的 update_progress。
//
// 同步下载在 80MB 的包上会挂住 HTTP 连接好几分钟，中间还不能取消；
// 改成「立刻返回 + SSE 推进度」，界面才能显示速度、剩余时间与节点。
func (s *Server) handleUpdateDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL   string `json:"url"`
		Name  string `json:"name"`
		Node  string `json:"node"`
		Size  int64  `json:"size"`
		Force bool   `json:"force"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.URL == "" {
		writeErr(w, 400, fmt.Errorf("缺少下载地址"))
		return
	}
	dl := s.updateDownloader()
	if dl.Busy() {
		writeErr(w, 409, fmt.Errorf("已经有一个下载在进行"))
		return
	}
	g := s.cfg.Get().GitHub
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	nodes := s.pickNodes(ctx, body.URL, body.Node, body.Force)
	eps := update.Endpoints(body.URL, nodes)
	if len(eps) == 0 {
		writeErr(w, 500, fmt.Errorf("没有可用的下载节点"))
		return
	}
	err := dl.Start(update.Job{
		URL:       body.URL,
		Name:      body.Name,
		Dir:       update.Dir(s.paths.Root),
		Token:     g.Token,
		Expect:    body.Size,
		Endpoints: eps,
		Custom:    g.Mirrors,
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "endpoints": len(eps),
		"first": eps[0].Name, "task": dl.Current(),
	})
}

// handleUpdateCancel 取消当前下载（保留 .part 供续传）。
func (s *Server) handleUpdateCancel(w http.ResponseWriter, r *http.Request) {
	s.updateDownloader().Cancel()
	writeJSON(w, map[string]any{"ok": true})
}

// handleUpdateApply 解压更新包并拉起辅助升级进程，随后退出主程序。
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	}
	if err := readJSON(r, &body); err != nil || body.Path == "" {
		writeErr(w, 400, fmt.Errorf("缺少 path"))
		return
	}
	// 正在上课时绝不退出：录制、转写、截图全在这个进程里，
	// 一条「有新版本」的提示不该把老师的整堂课弄断。
	if s.engine.Status().Running {
		writeErr(w, 409, fmt.Errorf("正在上课，结束课堂后再安装更新"))
		return
	}
	// 只允许安装更新目录里的包：这个路径来自请求体，
	// 不设限等于让任何本地页面都能让助手去解压任意 zip。
	updDir := update.Dir(s.paths.Root)
	if !withinDir(updDir, body.Path) {
		writeErr(w, 400, fmt.Errorf("更新包不在下载目录里"))
		return
	}
	if _, err := os.Stat(body.Path); err != nil {
		writeErr(w, 400, fmt.Errorf("更新包不存在，请重新下载"))
		return
	}
	if !s.applying.CompareAndSwap(false, true) {
		writeErr(w, 409, fmt.Errorf("正在安装更新，请稍候"))
		return
	}
	ver := strings.TrimSpace(body.Version)
	if ver == "" {
		ver = "unknown"
	}
	info, err := update.Apply(body.Path, s.paths.Root, ver)
	if err != nil {
		s.applying.Store(false)
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"helper":  info.HelperPID,
		"files":   info.Files,
		"to":      info.To,
		"log":     info.Log,
		"message": "升级助手已启动，程序即将退出并完成替换，完成后会自动重新打开",
	})
	// 留出时间把响应发回页面，再走优雅退出（会顺带停掉 whisper-server）。
	s.requestExit(1200 * time.Millisecond)
}

// withinDir 判断 path 是否在 dir 之内（防 ../ 穿越）。
func withinDir(dir, path string) bool {
	absDir, err1 := filepath.Abs(dir)
	absPath, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// requestExit 在 delay 之后退出程序。
//
// 优先走 main 注册的优雅退出（停托盘、停语音服务、落盘）；
// 没注册时才退回 os.Exit——直接 Exit 会把 whisper-server 留在机器上。
func (s *Server) requestExit(delay time.Duration) {
	go func() {
		time.Sleep(delay)
		s.exitMu.Lock()
		fn := s.exitFn
		s.exitMu.Unlock()
		if fn != nil {
			fn()
			return
		}
		os.Exit(0)
	}()
}

// SetExitHook 注册优雅退出回调，由 main 提供。
func (s *Server) SetExitHook(fn func()) {
	s.exitMu.Lock()
	s.exitFn = fn
	s.exitMu.Unlock()
}
