package capture

import (
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"time"

	"classnote/internal/config"
)

// Snapshot 是一次幻灯片快照。
type Snapshot struct {
	Image      *image.RGBA `json:"-"`
	PNG        []byte      `json:"-"`
	Hash       uint64      `json:"hash"`
	Index      int         `json:"index"`
	Source     string      `json:"source"` // com | window | manual
	App        string      `json:"app"`
	Title      string      `json:"title"`
	FilePath   string      `json:"filePath"`
	Width      int         `json:"width"`
	Height     int         `json:"height"`
	At         time.Time   `json:"at"`
	Cropped    bool        `json:"cropped"`
	WindowInfo WindowInfo  `json:"window"`
}

// Status 描述当前放映状态。
type Status struct {
	Presenting bool   `json:"presenting"`
	App        string `json:"app"`
	Index      int    `json:"index"`
	SlideCount int    `json:"slideCount"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	Detected   bool   `json:"detected"`
	LastError  string `json:"lastError,omitempty"`
}

// Detector 负责判断"是否在放映"以及"是否翻页"。
type Detector struct {
	mu       sync.Mutex
	cfg      config.CaptureConfig
	exporter *SlideExporter
	lastHash uint64
	lastIdx  int
	lastSnap *Snapshot
	present  bool
	appName  string
	title    string
	lastErr  string
	slideCnt int
	misses   int
}

// NewDetector 创建检测器。
func NewDetector(cfg config.CaptureConfig) *Detector {
	return &Detector{cfg: cfg, exporter: NewSlideExporter()}
}

// UpdateConfig 热更新配置。
func (d *Detector) UpdateConfig(cfg config.CaptureConfig) {
	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()
}

// Reset 清空上一次的翻页记录，强制下一次检测视为新页。
func (d *Detector) Reset() {
	d.mu.Lock()
	d.lastHash = 0
	d.lastIdx = -1
	d.lastSnap = nil
	d.mu.Unlock()
}

// Status 返回当前状态。
func (d *Detector) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return Status{
		Presenting: d.present,
		App:        d.appName,
		Index:      d.lastIdx,
		SlideCount: d.slideCnt,
		Title:      d.title,
		Source:     d.cfg.Mode,
		Detected:   d.present,
		LastError:  d.lastErr,
	}
}

// Probe 检测当前是否放映、是否发生翻页。
// 返回 (snapshot, changed, presenting)。
func (d *Detector) Probe() (*Snapshot, bool, bool) {
	d.mu.Lock()
	cfg := d.cfg
	lastIdx := d.lastIdx
	lastHash := d.lastHash
	d.mu.Unlock()

	// 1) 优先使用 COM：可以得到精确页码与高清导出，且无需逐帧比对。
	if cfg.ExportHighRes {
		info, err := d.exporter.Info()
		if err == nil && info.Available {
			d.mu.Lock()
			d.present = true
			d.appName = friendlyApp(info.App)
			d.title = info.Title
			d.slideCnt = info.SlideCount
			d.lastErr = ""
			d.misses = 0
			d.mu.Unlock()
			if info.SlideIndex != lastIdx {
				if path, idx, err2 := d.exporter.ExportCurrent(os.TempDir(), 1920, 1080); err2 == nil {
					img, derr := DecodeFile(path)
					_ = os.Remove(path)
					if derr == nil {
						snap := &Snapshot{
							Image: img, Hash: DHash(img), Index: idx, Source: "com",
							App: friendlyApp(info.App), Title: info.Title, FilePath: info.FilePath,
							Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), At: time.Now(),
						}
						snap.PNG, _ = EncodePNG(img)
						d.commit(snap)
						return snap, true, true
					}
				}
			}
			// 页码未变化，复用上次快照。
			d.mu.Lock()
			snap := d.lastSnap
			d.mu.Unlock()
			return snap, false, true
		}
	}

	// 2) 回退到窗口截图 + 感知哈希。
	win, ok := FindSlideShowWindow()
	if !ok {
		d.mu.Lock()
		d.misses++
		if d.misses > 2 {
			d.present = false
			d.appName = ""
			d.lastIdx = -1
			d.lastHash = 0
		}
		d.mu.Unlock()
		return nil, false, false
	}
	img, err := CaptureWindow(win.HWND)
	if err != nil {
		d.mu.Lock()
		d.lastErr = err.Error()
		d.mu.Unlock()
		return nil, false, d.present
	}
	cropped := false
	if cfg.CropBlackBars {
		if c := cropBlackBars(img); c != img {
			img = c
			cropped = true
		}
	}
	if cfg.MaxWidth > 0 {
		img = Resize(img, cfg.MaxWidth)
	}
	hash := DHash(img)
	changed := lastHash == 0 || Hamming(hash, lastHash) > cfg.DiffThreshold

	d.mu.Lock()
	d.present = true
	d.appName = friendlyApp(win.Process)
	d.title = win.Title
	d.misses = 0
	d.lastErr = ""
	d.mu.Unlock()

	if !changed {
		d.mu.Lock()
		snap := d.lastSnap
		d.mu.Unlock()
		return snap, false, true
	}
	snap := &Snapshot{
		Image: img, Hash: hash, Index: lastIdx + 1, Source: "window",
		App: friendlyApp(win.Process), Title: win.Title, WindowInfo: win,
		Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), At: time.Now(), Cropped: cropped,
	}
	snap.PNG, _ = EncodePNG(img)
	d.commit(snap)
	return snap, true, true
}

func (d *Detector) commit(snap *Snapshot) {
	d.mu.Lock()
	d.lastSnap = snap
	d.lastHash = snap.Hash
	d.lastIdx = snap.Index
	d.present = true
	d.mu.Unlock()
}

// CaptureNow 立即抓取当前幻灯片，忽略翻页判断。
func (d *Detector) CaptureNow() (*Snapshot, error) {
	info, err := d.exporter.Info()
	if err == nil && info.Available {
		path, idx, err2 := d.exporter.ExportCurrent(os.TempDir(), 1920, 1080)
		if err2 == nil {
			img, derr := DecodeFile(path)
			_ = os.Remove(path)
			if derr == nil {
				snap := &Snapshot{
					Image: img, Hash: DHash(img), Index: idx, Source: "com",
					App: friendlyApp(info.App), Title: info.Title, FilePath: info.FilePath,
					Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), At: time.Now(),
				}
				snap.PNG, _ = EncodePNG(img)
				d.mu.Lock()
				d.present = true
				d.appName = friendlyApp(info.App)
				d.title = info.Title
				d.lastErr = ""
				d.mu.Unlock()
				d.commit(snap)
				return snap, nil
			}
		}
	}
	win, ok := FindSlideShowWindow()
	if !ok {
		// 没有放映时抓全屏，作为手动"拍一下"。
		img, err := CaptureScreen()
		if err != nil {
			return nil, err
		}
		img = Resize(img, d.cfg.MaxWidth)
		snap := &Snapshot{Image: img, Hash: DHash(img), Source: "manual", At: time.Now(),
			Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), App: "屏幕"}
		snap.PNG, _ = EncodePNG(img)
		return snap, nil
	}
	img, err := CaptureWindow(win.HWND)
	if err != nil {
		return nil, err
	}
	cropped := false
	if d.cfg.CropBlackBars {
		if c := cropBlackBars(img); c != img {
			img = c
			cropped = true
		}
	}
	if d.cfg.MaxWidth > 0 {
		img = Resize(img, d.cfg.MaxWidth)
	}
	snap := &Snapshot{
		Image: img, Hash: DHash(img), Source: "window", App: friendlyApp(win.Process),
		Title: win.Title, WindowInfo: win, At: time.Now(), Cropped: cropped,
		Width: img.Bounds().Dx(), Height: img.Bounds().Dy(),
	}
	snap.PNG, _ = EncodePNG(img)
	d.commit(snap)
	return snap, nil
}

func friendlyApp(name string) string {
	switch strings.ToLower(name) {
	case "powerpnt.exe":
		return "PowerPoint"
	case "wpp.exe":
		return "WPS 演示"
	case "wps.exe":
		return "WPS Office"
	}
	return name
}

// FindSlideShowWindow 查找放映窗口。
func FindSlideShowWindow() (WindowInfo, bool) {
	var best WindowInfo
	bestArea := int32(0)
	for _, w := range ListWindows() {
		p := strings.ToLower(w.Process)
		isPPT := p == "powerpnt.exe" || p == "wpp.exe" || p == "wps.exe" || p == "pptview.exe"
		if !isPPT {
			continue
		}
		class := strings.ToLower(w.Class)
		title := w.Title
		fullscreen := w.Fullscreen
		match := class == "screenclass" ||
			strings.Contains(class, "slide") ||
			(fullscreen && (class == "wpp" || class == "wps" || strings.Contains(class, "present")))
		if !match {
			continue
		}
		// 主窗口标题通常包含文件名；放映窗口标题含"放映"或"Slide Show"。
		if strings.Contains(title, "放映") || strings.Contains(strings.ToLower(title), "slide show") {
			match = true
		}
		if !match {
			continue
		}
		area := w.Rect.Width() * w.Rect.Height()
		if area > bestArea {
			best, bestArea = w, area
		}
	}
	return best, bestArea > 0
}

// DescribeWindow 生成用于日志的窗口描述。
func DescribeWindow(w WindowInfo) string {
	return fmt.Sprintf("%s[%s] %dx%d @(%d,%d)", w.Process, w.Class,
		w.Rect.Width(), w.Rect.Height(), w.Rect.Left, w.Rect.Top)
}
