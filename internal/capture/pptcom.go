package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/go-ole/go-ole"

	"classnote/internal/logx"
)

// PPTInfo 描述当前放映状态。
type PPTInfo struct {
	Available    bool   `json:"available"`
	App          string `json:"app"`
	SlideIndex   int    `json:"slideIndex"`
	SlideCount   int    `json:"slideCount"`
	Title        string `json:"title"`
	FilePath     string `json:"filePath"`
	Presentation string `json:"presentation"`
}

// ErrNoSlideShow 表示当前没有放映中的幻灯片（属于正常状态，不是故障）。
var ErrNoSlideShow = fmt.Errorf("当前没有放映中的幻灯片")

// SlideExporter 通过 COM 从 PowerPoint / WPS 演示导出当前页。
type SlideExporter struct {
	mu            sync.Mutex
	ch            chan comReq
	disabledUntil time.Time
	fails         int
	lastLog       time.Time
	appName       string
	// stuck 记录被 Office 模态对话框卡住、再也不会退出的工作线程数。
	// 每个这样的线程还占着一个 OS 线程和一个 COM 公寓，永远不会回收，
	// 所以超过上限就彻底放弃 COM 通道，改用窗口类名与像素哈希检测。
	stuck int
	dead  bool
}

type comReq struct {
	fn    func(app *ole.IDispatch, progID string) (any, error)
	reply chan comResp
}

type comResp struct {
	val any
	err error
}

// NewSlideExporter 创建导出器。
func NewSlideExporter() *SlideExporter { return &SlideExporter{} }

var progIDs = []string{"PowerPoint.Application", "KWPP.Application", "kwpp.Application", "WPP.Application"}

// loop 是独占一个 OS 线程的 COM 工作线程，整个生命周期只初始化一次 COM。
func (e *SlideExporter) loop(ch chan comReq) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	defer ole.CoUninitialize()

	var app *ole.IDispatch
	var activeProg string

	attach := func() (*ole.IDispatch, string) {
		if app != nil {
			return app, activeProg
		}
		for _, pid := range progIDs {
			// 先取 ProgID 对应的 CLSID，再绑定到正在运行的实例。
			// 直接 CoGetObject("PowerPoint.Application") 会报"无效的语法"。
			g, err := ole.CLSIDFromProgID(pid)
			if err != nil || g == nil || g.String() == "{00000000-0000-0000-0000-000000000000}" {
				continue
			}
			unk, err := ole.GetActiveObject(g, ole.IID_IDispatch)
			if err != nil || unk == nil {
				continue
			}
			disp, err := unk.QueryInterface(ole.IID_IDispatch)
			// 无论成功与否都要释放 GetActiveObject 拿到的这份引用。
			// 1.2.5 只在失败分支 Release，成功路径每附着一次漏一个 COM 引用，
			// 反复重附着（PPT 被切走再切回来）会一直涨。
			unk.Release()
			if err != nil {
				continue
			}
			app = disp
			activeProg = pid
			return app, activeProg
		}
		return nil, ""
	}

	for req := range ch {
		a, pid := attach()
		if a == nil {
			req.reply <- comResp{err: ErrNoSlideShow}
			continue
		}
		val, err := func() (v any, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("COM 调用异常: %v", r)
					app = nil
					activeProg = ""
				}
			}()
			return req.fn(a, pid)
		}()
		if err != nil {
			// 对象可能已失效，下次重新附着。
			app = nil
			activeProg = ""
		}
		req.reply <- comResp{val: val, err: err}
	}
}

// call 把请求投递给 COM 工作线程；超时会放弃卡死的线程并进入短暂冷却，随后自愈。
func (e *SlideExporter) call(fn func(app *ole.IDispatch, progID string) (any, error), timeout time.Duration) (any, error) {
	e.mu.Lock()
	if e.dead {
		e.mu.Unlock()
		return nil, fmt.Errorf("COM 通道已停用")
	}
	if wait := time.Until(e.disabledUntil); wait > 0 {
		e.mu.Unlock()
		return nil, fmt.Errorf("COM 通道冷却中，%s 后重试", wait.Round(time.Second))
	}
	if e.ch == nil {
		e.ch = make(chan comReq, 8)
		go e.loop(e.ch)
	}
	ch := e.ch
	e.mu.Unlock()

	req := comReq{fn: fn, reply: make(chan comResp, 1)}
	select {
	case ch <- req:
	case <-time.After(2 * time.Second):
		e.markStuck("COM 请求队列已满")
		return nil, fmt.Errorf("COM 通道繁忙")
	}
	select {
	case r := <-req.reply:
		if r.err == nil {
			e.mu.Lock()
			e.fails = 0
			e.mu.Unlock()
		}
		return r.val, r.err
	case <-time.After(timeout):
		// 工作线程可能被 Office 的模态对话框阻塞：丢弃它，稍后用新线程重试。
		e.mu.Lock()
		if e.ch == ch {
			e.ch = nil
		}
		// 这个工作线程还被 Office 的模态对话框卡着，退不出去，
		// 线程和 COM 公寓都收不回来，只能记账并限制总数。
		e.stuck++
		if e.stuck >= 3 && !e.dead {
			e.dead = true
		}
		e.fails++
		if e.fails >= 2 {
			e.disabledUntil = time.Now().Add(8 * time.Second)
			e.fails = 0
		}
		logIt := time.Since(e.lastLog) > 10*time.Second
		if logIt {
			e.lastLog = time.Now()
		}
		dead := e.dead
		stuck := e.stuck
		e.mu.Unlock()
		if dead {
			logx.Warnf("capture", "COM 通道已卡死 %d 次，永久切换到窗口类名 / 像素哈希检测", stuck)
		}
		if logIt {
			logx.Warnf("capture", "COM 调用超时，已切换备用检测通道")
		}
		return nil, fmt.Errorf("COM 调用超时")
	}
}

func toDispatch(v *ole.VARIANT) (*ole.IDispatch, bool) {
	if v == nil {
		return nil, false
	}
	switch t := v.Value().(type) {
	case *ole.IDispatch:
		return t, t != nil
	case *ole.IUnknown:
		if t == nil {
			return nil, false
		}
		d, err := t.QueryInterface(ole.IID_IDispatch)
		if err != nil {
			return nil, false
		}
		return d, true
	}
	return nil, false
}

func propInt(d *ole.IDispatch, name string) (int, error) {
	v, err := d.GetProperty(name)
	if err != nil {
		return 0, err
	}
	defer v.Clear()
	switch n := v.Value().(type) {
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case int:
		return n, nil
	case float64:
		return int(n), nil
	}
	return 0, fmt.Errorf("属性 %s 不是整数", name)
}

func propString(d *ole.IDispatch, name string) string {
	v, err := d.GetProperty(name)
	if err != nil {
		return ""
	}
	defer v.Clear()
	if s, ok := v.Value().(string); ok {
		return s
	}
	return ""
}

// Info 查询放映状态。
func (e *SlideExporter) Info() (PPTInfo, error) {
	val, err := e.call(func(app *ole.IDispatch, progID string) (any, error) {
		wins, err := app.GetProperty("SlideShowWindows")
		if err != nil {
			return nil, err
		}
		defer wins.Clear()
		winDisp, ok := toDispatch(wins)
		if !ok {
			return nil, fmt.Errorf("无法访问放映窗口")
		}
		count, err := propInt(winDisp, "Count")
		if err != nil || count <= 0 {
			return nil, ErrNoSlideShow
		}
		item, err := winDisp.CallMethod("Item", int32(1))
		if err != nil {
			return nil, err
		}
		defer item.Clear()
		win, ok := toDispatch(item)
		if !ok {
			return nil, fmt.Errorf("无法访问放映窗口")
		}
		viewV, err := win.GetProperty("View")
		if err != nil {
			return nil, err
		}
		defer viewV.Clear()
		view, ok := toDispatch(viewV)
		if !ok {
			return nil, fmt.Errorf("无法访问放映视图")
		}
		slideV, err := view.GetProperty("Slide")
		if err != nil {
			return nil, err
		}
		defer slideV.Clear()
		slide, ok := toDispatch(slideV)
		if !ok {
			return nil, fmt.Errorf("无法访问当前幻灯片")
		}
		idx, err := propInt(slide, "SlideIndex")
		if err != nil {
			return nil, err
		}
		info := PPTInfo{Available: true, App: progID, SlideIndex: idx}
		if pv, e2 := slide.GetProperty("Parent"); e2 == nil {
			defer pv.Clear()
			if pres, ok := toDispatch(pv); ok {
				info.Title = propString(pres, "Name")
				info.FilePath = propString(pres, "FullName")
				info.Presentation = info.Title
				if sv, e3 := pres.GetProperty("Slides"); e3 == nil {
					defer sv.Clear()
					if slides, ok := toDispatch(sv); ok {
						info.SlideCount, _ = propInt(slides, "Count")
					}
				}
			}
		}
		return info, nil
	}, 2500*time.Millisecond)
	if err != nil {
		return PPTInfo{}, err
	}
	info, _ := val.(PPTInfo)
	return info, nil
}

// ExportCurrent 导出当前页为 PNG，返回图片路径。
func (e *SlideExporter) ExportCurrent(dir string, width, height int) (string, int, error) {
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	out := filepath.Join(dir, fmt.Sprintf("com-%d.png", time.Now().UnixNano()))
	val, err := e.call(func(app *ole.IDispatch, progID string) (any, error) {
		wins, err := app.GetProperty("SlideShowWindows")
		if err != nil {
			return nil, err
		}
		defer wins.Clear()
		winDisp, ok := toDispatch(wins)
		if !ok {
			return nil, fmt.Errorf("无法访问放映窗口")
		}
		count, _ := propInt(winDisp, "Count")
		if count <= 0 {
			return nil, ErrNoSlideShow
		}
		item, err := winDisp.CallMethod("Item", int32(1))
		if err != nil {
			return nil, err
		}
		defer item.Clear()
		win, ok := toDispatch(item)
		if !ok {
			return nil, fmt.Errorf("无法访问放映窗口")
		}
		viewV, err := win.GetProperty("View")
		if err != nil {
			return nil, err
		}
		defer viewV.Clear()
		view, ok := toDispatch(viewV)
		if !ok {
			return nil, fmt.Errorf("无法访问放映视图")
		}
		slideV, err := view.GetProperty("Slide")
		if err != nil {
			return nil, err
		}
		defer slideV.Clear()
		slide, ok := toDispatch(slideV)
		if !ok {
			return nil, fmt.Errorf("无法访问当前幻灯片")
		}
		idx, _ := propInt(slide, "SlideIndex")
		if _, err := slide.CallMethod("Export", out, "PNG", int32(width), int32(height)); err != nil {
			return nil, err
		}
		return idx, nil
	}, 4000*time.Millisecond)
	if err != nil {
		return "", 0, err
	}
	idx, _ := val.(int)
	if _, statErr := os.Stat(out); statErr != nil {
		return "", idx, fmt.Errorf("导出文件不存在")
	}
	return out, idx, nil
}

// markStuck 记录一次"通道被占死"，累计到上限就永久停用 COM。
func (e *SlideExporter) markStuck(why string) {
	e.mu.Lock()
	e.stuck++
	if e.stuck >= 3 && !e.dead {
		e.dead = true
	}
	stuck, dead := e.stuck, e.dead
	if e.ch != nil {
		e.ch = nil
	}
	e.mu.Unlock()
	logx.Debugf("capture", "%s（累计 %d 次）", why, stuck)
	if dead {
		logx.Warnf("capture", "COM 通道已停用，改用窗口类名 / 像素哈希检测")
	}
}
