package app

import (
	"image"
	"image/color"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"classnote/internal/logx"
)

// 悬浮球：常驻桌面角落的小窗，显示课堂状态，点开主界面。
//
// 用分层窗口（UpdateLayeredWindow）直接绘制 RGBA，支持圆角与半透明，
// 窄边、置顶、不进任务栏；悬停展开成小胶囊显示科目与计时。
var (
	fbUser32   = windows.NewLazySystemDLL("user32.dll")
	fbGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	fbKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procFBRegisterClass   = fbUser32.NewProc("RegisterClassExW")
	procFBCreateWindow    = fbUser32.NewProc("CreateWindowExW")
	procFBDefWindowProc   = fbUser32.NewProc("DefWindowProcW")
	procFBDestroyWindow   = fbUser32.NewProc("DestroyWindow")
	procFBGetMessage      = fbUser32.NewProc("GetMessageW")
	procFBTranslate       = fbUser32.NewProc("TranslateMessage")
	procFBDispatch        = fbUser32.NewProc("DispatchMessageW")
	procFBPostQuit        = fbUser32.NewProc("PostQuitMessage")
	procFBPostMessage     = fbUser32.NewProc("PostMessageW")
	procFBSetTimer        = fbUser32.NewProc("SetTimer")
	procFBKillTimer       = fbUser32.NewProc("KillTimer")
	procFBUpdateLayered   = fbUser32.NewProc("UpdateLayeredWindow")
	procFBGetDC           = fbUser32.NewProc("GetDC")
	procFBReleaseDC       = fbUser32.NewProc("ReleaseDC")
	procFBSetWindowPos    = fbUser32.NewProc("SetWindowPos")
	procFBShowWindow      = fbUser32.NewProc("ShowWindow")
	procFBSendMessage     = fbUser32.NewProc("SendMessageW")
	procFBGetCursorPos    = fbUser32.NewProc("GetCursorPos")
	procFBGetSystemM      = fbUser32.NewProc("GetSystemMetrics")
	procFBSetCapture      = fbUser32.NewProc("SetCapture")
	procFBReleaseCapture  = fbUser32.NewProc("ReleaseCapture")
	procFBGetClientRect   = fbUser32.NewProc("GetClientRect")
	procFBGetWindowRect   = fbUser32.NewProc("GetWindowRect")
	procFBTrackMouseEvent = fbUser32.NewProc("TrackMouseEvent")

	procFBCreateDC     = fbGdi32.NewProc("CreateCompatibleDC")
	procFBCreateDIB    = fbGdi32.NewProc("CreateDIBSection")
	procFBSelectObject = fbGdi32.NewProc("SelectObject")
	procFBDeleteDC     = fbGdi32.NewProc("DeleteDC")
	procFBDeleteObject = fbGdi32.NewProc("DeleteObject")
	procFBCreateSolid  = fbGdi32.NewProc("CreateSolidBrush")
	procFBCreateFontW  = fbGdi32.NewProc("CreateFontW")
	procFBSetBkMode    = fbGdi32.NewProc("SetBkMode")
	procFBSetTextColor = fbGdi32.NewProc("SetTextColor")
	procFBTextOutW     = fbGdi32.NewProc("TextOutW")
	procFBGetModule    = fbKernel32.NewProc("GetModuleHandleW")
)

const (
	fbClass       = "ClassNoteFloatBall"
	fbWMUser      = 0x8001 // 状态更新
	fbTimerRender = 1

	wmNCLButtonDown = 0x00A1
	htCaption       = 2
	wmMouseMove     = 0x0200
	wmMouseLeave    = 0x02A3
	fbLButtonDown   = 0x0201
	fbLButtonUp     = 0x0202
	fbRButtonUp     = 0x0205
	fbLButtonDbl    = 0x0203
	wmTimer         = 0x0113

	wsExLayered    = 0x00080000
	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsPopup        = 0x80000000
	wsVisible      = 0x10000000

	ulwAlpha       = 0x00000002
	swpNoMove      = 0x0002
	swpNoSize      = 0x0001
	swpNoActivate  = 0x0010
	swpShowWindow  = 0x0040
	swShowNoActive = 4

	smCxScreen = 0
	smCyScreen = 1

	// 动画
	fbAnimFrames = 10
)

// FloatBall 状态。
type FloatBallStatus struct {
	Running bool
	Subject string
	Elapsed int
	Level   float64 // 音量 0-1，用于呼吸灯
}

// FloatBallOptions 是悬浮球回调。
type FloatBallOptions struct {
	OpenMain func()
	OpenMenu func()
	Status   func() FloatBallStatus
}

// FloatBall 是桌面悬浮球。
type FloatBall struct {
	opts FloatBallOptions

	mu       sync.Mutex
	hwnd     windows.Handle
	expanded bool
	anim     int // 0..fbAnimFrames，0=收起
	status   FloatBallStatus
	x, y     int32 // 窗口左上角（物理像素），展开时靠右边缘定位
	winW     int32 // 当前窗口宽度，用于展开/收拢时保持右边缘

	// 拖动状态：按下记录偏移，移动时跟手；位移超过阈值才算拖动，否则视为点击。
	dragging   bool
	dragMoved  bool
	dragOffX   int32
	dragOffY   int32
	dragStartX int32
	dragStartY int32

	ready chan struct{}
	done  chan struct{}
	once  sync.Once
}

var fbInst *FloatBall

// NewFloatBall 创建悬浮球。
func NewFloatBall(opts FloatBallOptions) *FloatBall {
	return &FloatBall{opts: opts, ready: make(chan struct{}), done: make(chan struct{}), winW: 64}
}

// Start 启动悬浮球（异步）。
func (f *FloatBall) Start() error {
	fbInst = f
	go f.run()
	select {
	case <-f.ready:
		return nil
	case <-f.done:
		return nil // 悬浮球失败不阻塞主程序
	}
}

// Stop 关闭悬浮球。
func (f *FloatBall) Stop() {
	f.once.Do(func() {
		f.mu.Lock()
		h := f.hwnd
		f.mu.Unlock()
		if h != 0 {
			procFBPostMessage.Call(uintptr(h), wmDestroy, 0, 0)
		}
		select {
		case <-f.done:
		case <-time.After(2 * time.Second):
		}
	})
}

// SetStatus 更新悬浮球显示。
func (f *FloatBall) SetStatus(st FloatBallStatus) {
	f.mu.Lock()
	f.status = st
	f.mu.Unlock()
}

func (f *FloatBall) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(f.done)

	hInst, _, _ := procFBGetModule.Call(0)
	className, _ := windows.UTF16PtrFromString(fbClass)

	wc := wndClassEx{
		Style:     csHRedraw | csVRedraw,
		WndProc:   fbWndProcAddr(),
		Instance:  windows.Handle(hInst),
		ClassName: className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procFBRegisterClass.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		logx.Warnf("float", "注册悬浮球窗口失败: %v", err)
		return
	}

	// 默认停靠屏幕右上角（可拖动）
	sw, _, _ := procFBGetSystemM.Call(smCxScreen)
	sh, _, _ := procFBGetSystemM.Call(smCyScreen)
	x := int32(sw) - 96
	y := int32(16)

	hwnd, _, err := procFBCreateWindow.Call(wsExLayered|wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(className)), 0, wsPopup|wsVisible,
		uintptr(x), uintptr(y), 64, 64,
		0, 0, hInst, 0)
	if hwnd == 0 {
		logx.Warnf("float", "创建悬浮球失败: %v", err)
		return
	}
	// 分层窗口要等第一次 UpdateLayeredWindow 才有内容，先不激活地显示出来。
	procFBShowWindow.Call(hwnd, swShowNoActive)
	var initRect struct{ L, T, R, B int32 }
	procFBGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&initRect)))
	logx.Infof("float", "悬浮球窗口已创建 %d,%d %dx%d（屏幕 %dx%d）",
		initRect.L, initRect.T, initRect.R-initRect.L, initRect.B-initRect.T, sw, sh)
	f.mu.Lock()
	f.hwnd = windows.Handle(hwnd)
	f.x = int32(x)
	f.y = int32(y)
	f.winW = 64
	f.mu.Unlock()

	procFBSetTimer.Call(hwnd, fbTimerRender, 66, 0) // ~15fps 动画
	close(f.ready)

	var m winMsg
	for {
		r, _, _ := procFBGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procFBTranslate.Call(uintptr(unsafe.Pointer(&m)))
		procFBDispatch.Call(uintptr(unsafe.Pointer(&m)))
	}
	logx.Infof("float", "悬浮球已退出")
}

func fbWndProcAddr() uintptr {
	return syscall.NewCallback(fbWndProc)
}

func fbWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	f := fbInst
	switch uint32(msg) {
	case wmTimer:
		if f != nil {
			f.tick(windows.Handle(hwnd))
		}
		return 0
	case wmMouseMove:
		if f != nil {
			// 拖动中：窗口跟手移动
			f.mu.Lock()
			dragging := f.dragging
			if dragging {
				var cp struct{ X, Y int32 }
				procFBGetCursorPos.Call(uintptr(unsafe.Pointer(&cp)))
				nx := cp.X + f.dragOffX
				ny := cp.Y + f.dragOffY
				if abs32(cp.X-f.dragStartX) > 3 || abs32(cp.Y-f.dragStartY) > 3 {
					f.dragMoved = true
				}
				f.x = nx
				f.y = ny
				f.mu.Unlock()
				procFBSetWindowPos.Call(hwnd, 0, uintptr(uint32(nx)), uintptr(uint32(ny)),
					0, 0, swpNoSize|swpNoActivate|swpShowWindow)
				return 0
			}
			was := f.expanded
			f.mu.Unlock()
			if !was {
				// TrackMouseLeave，移出时收起
				type tme struct {
					Size, Flags uint32
					HWnd        uintptr
				}
				procFBTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme{Size: 16, Flags: 2, HWnd: hwnd})))
				f.mu.Lock()
				f.expanded = true
				f.mu.Unlock()
			}
		}
		return 0
	case wmMouseLeave:
		if f != nil {
			f.mu.Lock()
			f.expanded = false
			f.mu.Unlock()
		}
		return 0
	case fbLButtonDown:
		// 开始拖动：记录鼠标相对窗口的偏移，此后移动跟手
		if f != nil {
			var cp struct{ X, Y int32 }
			procFBGetCursorPos.Call(uintptr(unsafe.Pointer(&cp)))
			var wr struct{ L, T, R, B int32 }
			procFBGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
			f.mu.Lock()
			f.dragging = true
			f.dragMoved = false
			f.dragOffX = wr.L - cp.X
			f.dragOffY = wr.T - cp.Y
			f.dragStartX = cp.X
			f.dragStartY = cp.Y
			f.mu.Unlock()
			procFBSetCapture.Call(hwnd)
		}
		return 0
	case fbLButtonUp:
		procFBReleaseCapture.Call()
		if f != nil {
			f.mu.Lock()
			wasDrag := f.dragMoved
			f.dragging = false
			f.mu.Unlock()
			// 没有实际位移才算点击：短按打开主界面
			if !wasDrag && f.opts.OpenMain != nil {
				go f.opts.OpenMain()
			}
		}
		return 0
	case fbLButtonDbl:
		return 0
	case fbRButtonUp:
		if f != nil && f.opts.OpenMenu != nil {
			go f.opts.OpenMenu()
		}
		return 0
	case wmDestroy: // 与托盘共用 0x0002
		procFBKillTimer.Call(hwnd, fbTimerRender)
		procFBDestroyWindow.Call(hwnd)
		procFBPostQuit.Call(0)
		return 0
	}
	r, _, _ := procFBDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}

// tick 每帧：拉状态、做展开动画、重绘。
func (f *FloatBall) tick(hwnd windows.Handle) {
	if f.opts.Status != nil {
		f.mu.Lock()
		f.status = f.opts.Status()
		f.mu.Unlock()
	}

	f.mu.Lock()
	target := 0
	if f.expanded {
		target = fbAnimFrames
	}
	if f.anim < target {
		f.anim++
	} else if f.anim > target {
		f.anim--
	}
	anim := f.anim
	st := f.status
	f.mu.Unlock()

	img := renderBall(anim, st)
	f.present(hwnd, img)
}

// present 把 RGBA 图贴到分层窗口上。
func (f *FloatBall) present(hwnd windows.Handle, img *image.RGBA) {
	b := img.Bounds()
	w, h := int32(b.Dx()), int32(b.Dy())

	hdc, _, _ := procFBGetDC.Call(uintptr(hwnd))
	if hdc == 0 {
		return
	}
	defer procFBReleaseDC.Call(uintptr(hwnd), hdc)
	memDC, _, _ := procFBCreateDC.Call(hdc)
	if memDC == 0 {
		return
	}
	defer procFBDeleteDC.Call(memDC)

	// 32 位 DIB
	type bmiHeader struct {
		Size          uint32
		Width, Height int32
		Planes        uint16
		BitCount      uint16
		Compression   uint32
		SizeImage     uint32
		XPels, YPels  int32
		ClrUsed       uint32
		ClrImportant  uint32
	}
	type bmi struct {
		Header bmiHeader
		Masks  [3]uint32
	}
	bi := bmi{Header: bmiHeader{
		Size: 40, Width: w, Height: -h, Planes: 1, BitCount: 32,
		Compression: 0, SizeImage: uint32(w * h * 4),
	}}
	var bits unsafe.Pointer
	hbmp, _, _ := procFBCreateDIB.Call(memDC, uintptr(unsafe.Pointer(&bi)), 0,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbmp == 0 {
		return
	}
	defer procFBDeleteObject.Call(hbmp)

	// 拷贝像素（BGRA）
	dst := unsafe.Slice((*byte)(bits), int(w*h*4))
	src := img.Pix
	for i := 0; i < len(dst); i += 4 {
		dst[i] = src[i]
		dst[i+1] = src[i+1]
		dst[i+2] = src[i+2]
		dst[i+3] = src[i+3]
	}

	old, _, _ := procFBSelectObject.Call(memDC, hbmp)
	defer procFBSelectObject.Call(memDC, old)

	type point struct{ X, Y int32 }
	type size struct{ W, H int32 }
	type blendFn struct {
		Op             byte
		Flags          byte
		Alpha          byte
		Format         byte
		SourceConstant byte
		AlphaFormat    byte
		_              [2]byte
	}
	// BLENDFUNCTION: BlendOp, BlendFlags, SourceConstantAlpha, AlphaFormat
	type blend struct {
		Op, Flags, Alpha, Format byte
	}
	srcPt := point{}
	dstPt := point{X: 0, Y: 0}
	sz := size{W: w, H: h}
	bf := blend{Op: 0, Flags: 0, Alpha: 255, Format: 1} // AC_SRC_ALPHA

	procFBUpdateLayered.Call(uintptr(hwnd), hdc,
		uintptr(unsafe.Pointer(&dstPt)), uintptr(unsafe.Pointer(&sz)),
		memDC, uintptr(unsafe.Pointer(&srcPt)), 0,
		uintptr(unsafe.Pointer(&bf)), ulwAlpha)

	// 尺寸变化时保持右边缘不动，向左长出来；否则停靠在右上角的球
	// 展开后会被屏幕右边裁掉。位置由 f.x/f.y 维护，避免 DPI 下
	// GetWindowRect 返回 0,0 把窗口挪到左上角。
	if int32(w) != f.winW {
		f.x -= int32(w) - f.winW
		if f.x < 0 {
			f.x = 0
		}
		logx.Infof("float", "悬浮球尺寸 %d→%d，位置 %d,%d", f.winW, w, f.x, f.y)
		procFBSetWindowPos.Call(uintptr(hwnd), 0, uintptr(uint32(f.x)), uintptr(uint32(f.y)),
			uintptr(w), uintptr(h), swpNoActivate|swpShowWindow)
		f.winW = int32(w)
	}
}

// renderBall 画悬浮球：收起是 64x64 圆，展开是 220x64 胶囊。
func renderBall(anim int, st FloatBallStatus) *image.RGBA {
	k := float64(anim) / float64(fbAnimFrames) // 0..1
	w := int(64 + (220-64)*k)
	h := 64
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	bg := color.RGBA{28, 26, 24, 235}
	if st.Running {
		bg = color.RGBA{120, 52, 36, 235} // 记录中偏红
	}
	// 胶囊背景
	drawRounded(img, image.Rect(0, 0, w, h), 32, bg)

	// 左侧圆点（状态灯）
	dot := color.RGBA{120, 200, 140, 255}
	if st.Running {
		// 呼吸灯：随音量/时间脉动
		pulse := 0.6 + 0.4*float64(time.Now().UnixMilli()%1000)/1000.0
		dot = color.RGBA{240, 90, 70, uint8(160 + 90*pulse)}
	}
	drawCircle(img, 32, 32, 9, dot)

	if k > 0.25 {
		// 展开：画科目与计时
		alpha := uint8(255 * (k - 0.25) / 0.75)
		ink := color.RGBA{245, 240, 232, alpha}
		subj := st.Subject
		if subj == "" {
			subj = "空闲"
		}
		if !st.Running {
			subj = "点击开始记录"
		}
		drawText(img, 52, 20, subj, ink)
		if st.Running {
			drawText(img, 52, 38, clockText(st.Elapsed), color.RGBA{210, 195, 180, alpha})
		} else {
			drawText(img, 52, 38, "ClassNote", color.RGBA{180, 165, 150, alpha})
		}
	}
	return img
}

func clockText(sec int) string {
	if sec < 0 {
		sec = 0
	}
	m, s := sec/60, sec%60
	h := sec / 3600
	if h > 0 {
		return itoa(h) + ":" + pad2(m%60) + ":" + pad2(s)
	}
	return pad2(m) + ":" + pad2(s)
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// drawRounded 画圆角矩形（简单逐像素）。
func drawRounded(img *image.RGBA, r image.Rectangle, radius int, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if insideRounded(x, y, r, radius) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func insideRounded(x, y int, r image.Rectangle, radius int) bool {
	x0, y0, x1, y1 := r.Min.X, r.Min.Y, r.Max.X, r.Max.Y
	if x < x0 || x >= x1 || y < y0 || y >= y1 {
		return false
	}
	rx := radius
	ry := radius
	if rx > (x1-x0)/2 {
		rx = (x1 - x0) / 2
	}
	if ry > (y1-y0)/2 {
		ry = (y1 - y0) / 2
	}
	// 四角
	if x < x0+rx && y < y0+ry {
		dx, dy := x0+rx-x, y0+ry-y
		return dx*dx+dy*dy <= rx*ry
	}
	if x >= x1-rx && y < y0+ry {
		dx, dy := x-(x1-rx-1), y0+ry-y
		return dx*dx+dy*dy <= rx*ry
	}
	if x < x0+rx && y >= y1-ry {
		dx, dy := x0+rx-x, y-(y1-ry-1)
		return dx*dx+dy*dy <= rx*ry
	}
	if x >= x1-rx && y >= y1-ry {
		dx, dy := x-(x1-rx-1), y-(y1-ry-1)
		return dx*dx+dy*dy <= rx*ry
	}
	return true
}

func drawCircle(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

// drawText 用位图字体画简短文字（只支持需要的字符，中文走 GDI 太重，
// 这里用极简 5x7 点阵覆盖 ASCII，中文字符用方块占位——悬浮球只放短标签）。
// 悬浮球文字用 GDI 绘制。之前用 5x7 点阵，中文只能画成小方块，
// 用户看到的就是一排“乱码”；这里改用微软雅黑，和界面字体保持一致。
var (
	txtDC   uintptr
	txtBmp  uintptr
	txtFont uintptr
	txtBits unsafe.Pointer
	txtW    = 240
	txtH    = 40
)

func ensureTextGDI() bool {
	if txtDC != 0 {
		return true
	}
	dc, _, _ := procFBCreateDC.Call(0)
	if dc == 0 {
		return false
	}
	type bmiHeader struct {
		Size          uint32
		Width, Height int32
		Planes        uint16
		BitCount      uint16
		Compression   uint32
		SizeImage     uint32
		XPels, YPels  int32
		ClrUsed       uint32
		ClrImportant  uint32
	}
	type bmi struct {
		Header bmiHeader
		Masks  [3]uint32
	}
	bi := bmi{Header: bmiHeader{
		Size: 40, Width: int32(txtW), Height: -int32(txtH), Planes: 1, BitCount: 32,
		Compression: 0, SizeImage: uint32(txtW * txtH * 4),
	}}
	var bits unsafe.Pointer
	bmp, _, _ := procFBCreateDIB.Call(dc, uintptr(unsafe.Pointer(&bi)), 0,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		procFBDeleteDC.Call(dc)
		return false
	}
	procFBSelectObject.Call(dc, bmp)
	// -14px，微软雅黑 UI；DEFAULT_CHARSET + ANTIALIASED_QUALITY 保证中文有字。
	face, _ := windows.UTF16PtrFromString("Microsoft YaHei UI")
	font, _, _ := procFBCreateFontW.Call(uintptr(0xFFFFFFF2), 0, 0, 0, 500, 0, 0, 0,
		1, 0, 0, 4, 0, uintptr(unsafe.Pointer(face)))
	if font == 0 {
		procFBDeleteObject.Call(bmp)
		procFBDeleteDC.Call(dc)
		return false
	}
	procFBSelectObject.Call(dc, font)
	procFBSetBkMode.Call(dc, 1) // TRANSPARENT
	txtDC, txtBmp, txtFont, txtBits = dc, bmp, font, bits
	return true
}

func drawText(img *image.RGBA, x, y int, s string, c color.RGBA) {
	if s == "" || c.A == 0 || !ensureTextGDI() {
		return
	}
	buf := unsafe.Slice((*byte)(txtBits), txtW*txtH*4)
	for i := range buf {
		buf[i] = 0
	}
	u, _ := windows.UTF16FromString(s)
	if len(u) <= 1 {
		return
	}
	procFBSetTextColor.Call(txtDC, 0xFFFFFF)
	procFBTextOutW.Call(txtDC, 0, 0, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1))

	// GDI 在透明底上写入的是“覆盖率 × 文字色”，按覆盖率把文字混进胶囊。
	for row := 0; row < txtH; row++ {
		for col := 0; col < txtW; col++ {
			i := (row*txtW + col) * 4
			cov := buf[i]
			if buf[i+1] > cov {
				cov = buf[i+1]
			}
			if buf[i+2] > cov {
				cov = buf[i+2]
			}
			if cov == 0 {
				continue
			}
			px, py := x+col, y+row
			if !image.Pt(px, py).In(img.Bounds()) {
				continue
			}
			bg := img.RGBAAt(px, py)
			// image.RGBA 存的是预乘 alpha；文字不能超过底色 alpha，
			// 否则 PNG/分层窗口会因为“颜色 > alpha”出现蓝紫色溢出。
			ca := c.A
			if bg.A < ca {
				ca = bg.A
			}
			a := float64(cov) / 255 * float64(ca) / 255
			img.SetRGBA(px, py, color.RGBA{
				R: uint8(float64(c.R)*a + float64(bg.R)*(1-a)),
				G: uint8(float64(c.G)*a + float64(bg.G)*(1-a)),
				B: uint8(float64(c.B)*a + float64(bg.B)*(1-a)),
				A: bg.A,
			})
		}
	}
}

// FloatOpenFolder 打开目录（悬浮球菜单用）。
func FloatOpenFolder(dir string) error {
	return OpenFolder(dir)
}
