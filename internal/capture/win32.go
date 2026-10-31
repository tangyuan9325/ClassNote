// Package capture 提供 Windows 窗口枚举、屏幕抓取与幻灯片检测能力。
package capture

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	psapi    = windows.NewLazySystemDLL("psapi.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")

	procEnumWindows        = user32.NewProc("EnumWindows")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procGetClassNameW      = user32.NewProc("GetClassNameW")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
	procGetForegroundWnd   = user32.NewProc("GetForegroundWindow")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procGetDC              = user32.NewProc("GetDC")
	procGetWindowDC        = user32.NewProc("GetWindowDC")
	procPrintWindow        = user32.NewProc("PrintWindow")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procSetProcessDPI      = user32.NewProc("SetProcessDpiAwarenessContext")
	procMonitorFromWindow  = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW    = user32.NewProc("GetMonitorInfoW")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procGetDIBits              = gdi32.NewProc("GetDIBits")

	procOpenProcess     = kernel32.NewProc("OpenProcess")
	procCloseHandle     = kernel32.NewProc("CloseHandle")
	procQueryFullImgNmW = kernel32.NewProc("QueryFullProcessImageNameW")
)

const (
	srcCopy             = 0x00CC0020
	biRGB               = 0
	dibRGBColors        = 0
	smCXScreen          = 0
	smCYScreen          = 1
	processQueryLimited = 0x1000
)

// Rect 是屏幕矩形。
type Rect struct {
	Left, Top, Right, Bottom int32
}

// Width 返回宽度。
func (r Rect) Width() int32 { return r.Right - r.Left }

// Height 返回高度。
func (r Rect) Height() int32 { return r.Bottom - r.Top }

// WindowInfo 描述一个顶层窗口。
type WindowInfo struct {
	HWND       uintptr `json:"hwnd"`
	Title      string  `json:"title"`
	Class      string  `json:"class"`
	PID        uint32  `json:"pid"`
	Process    string  `json:"process"`
	Rect       Rect    `json:"rect"`
	Visible    bool    `json:"visible"`
	Fullscreen bool    `json:"fullscreen"`
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

var dpiOnce sync.Once

// InitDPI 让进程感知高 DPI，保证截图坐标正确。
func InitDPI() {
	dpiOnce.Do(func() {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
		_, _, _ = procSetProcessDPI.Call(uintptr(^uintptr(3)))
	})
}

// ScreenSize 返回主屏分辨率。
func ScreenSize() (int, int) {
	w, _, _ := procGetSystemMetrics.Call(smCXScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYScreen)
	return int(w), int(h)
}

func windowText(hwnd uintptr) string {
	buf := make([]uint16, 512)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}

func windowClass(hwnd uintptr) string {
	buf := make([]uint16, 256)
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}

func windowRect(hwnd uintptr) Rect {
	var r struct{ L, T, R, B int32 }
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return Rect{r.L, r.T, r.R, r.B}
}

func windowPID(hwnd uintptr) uint32 {
	var pid uint32
	procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func windowProcess(pid uint32) string {
	h, _, _ := procOpenProcess.Call(processQueryLimited, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	ret, _, _ := procQueryFullImgNmW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ret == 0 {
		return ""
	}
	full := syscall.UTF16ToString(buf[:size])
	return filepath.Base(full)
}

var enumMu sync.Mutex
var enumResult []WindowInfo

// ListWindows 枚举所有可见顶层窗口。
func ListWindows() []WindowInfo {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumResult = enumResult[:0]
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1
		}
		r := windowRect(hwnd)
		if r.Width() <= 0 || r.Height() <= 0 {
			return 1
		}
		pid := windowPID(hwnd)
		enumResult = append(enumResult, WindowInfo{
			HWND:    hwnd,
			Title:   windowText(hwnd),
			Class:   windowClass(hwnd),
			PID:     pid,
			Process: windowProcess(pid),
			Rect:    r,
			Visible: true,
		})
		return 1
	})
	procEnumWindows.Call(cb, 0)
	out := make([]WindowInfo, len(enumResult))
	copy(out, enumResult)
	sw, sh := ScreenSize()
	for i := range out {
		if int(out[i].Rect.Width()) >= sw && int(out[i].Rect.Height()) >= sh {
			out[i].Fullscreen = true
		}
	}
	return out
}

// FindProcess 判断进程是否在运行，返回匹配的进程路径列表。
func FindProcess(names ...string) []string {
	targets := make(map[string]bool, len(names))
	for _, n := range names {
		targets[strings.ToLower(n)] = true
	}
	seen := map[uint32]bool{}
	var out []string
	for _, w := range ListWindows() {
		if seen[w.PID] {
			continue
		}
		seen[w.PID] = true
		if targets[strings.ToLower(w.Process)] {
			out = append(out, w.Process)
		}
	}
	return out
}

// CaptureRect 抓取屏幕上指定矩形区域。
func CaptureRect(r Rect) (*image.RGBA, error) {
	InitDPI()
	w, h := int(r.Width()), int(r.Height())
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("无效的截图区域 %dx%d", w, h)
	}
	if w > 12000 || h > 12000 {
		return nil, fmt.Errorf("截图区域过大 %dx%d", w, h)
	}
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("无法获取屏幕 DC")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("无法创建内存 DC")
	}
	defer procDeleteDC.Call(memDC)

	bmp, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, fmt.Errorf("无法创建位图")
	}
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(memDC, bmp)
	defer procSelectObject.Call(memDC, old)

	ret, _, err := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), screenDC,
		uintptr(r.Left), uintptr(r.Top), srcCopy)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt 失败: %v", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(w),
			Height:      -int32(h), // 自顶向下
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
			SizeImage:   uint32(w * h * 4),
		},
	}
	n, _, _ := procGetDIBits.Call(memDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&img.Pix[0])),
		uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	if n == 0 {
		return nil, fmt.Errorf("GetDIBits 失败")
	}
	// GDI 输出为 BGRA，转换为 RGBA。
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2] = img.Pix[i+2], img.Pix[i]
	}
	return img, nil
}

// CaptureWindow 优先用 PrintWindow 直接渲染目标窗口（不受其他置顶窗口遮挡），
// 失败或结果为空时回退到屏幕 BitBlt。
func CaptureWindow(hwnd uintptr) (*image.RGBA, error) {
	r := windowRect(hwnd)
	if r.Width() <= 0 || r.Height() <= 0 {
		return nil, fmt.Errorf("窗口尺寸无效")
	}
	if img, err := captureWindowPrint(hwnd, r); err == nil && !isBlank(img) {
		return img, nil
	}
	img, err := CaptureRect(r)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// captureWindowPrint 使用 PrintWindow + PW_RENDERFULLCONTENT 抓取窗口内容。
func captureWindowPrint(hwnd uintptr, r Rect) (*image.RGBA, error) {
	const pwRenderFullContent = 0x00000002
	w, h := int(r.Width()), int(r.Height())
	if w <= 0 || h <= 0 || w > 12000 || h > 12000 {
		return nil, fmt.Errorf("窗口尺寸异常")
	}
	hdc, _, _ := procGetWindowDC.Call(hwnd)
	if hdc == 0 {
		return nil, fmt.Errorf("无法获取窗口 DC")
	}
	defer procReleaseDC.Call(hwnd, hdc)
	memDC, _, _ := procCreateCompatibleDC.Call(hdc)
	if memDC == 0 {
		return nil, fmt.Errorf("无法创建内存 DC")
	}
	defer procDeleteDC.Call(memDC)
	bmp, _, _ := procCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, fmt.Errorf("无法创建位图")
	}
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(memDC, bmp)
	defer procSelectObject.Call(memDC, old)
	ret, _, _ := procPrintWindow.Call(hwnd, memDC, pwRenderFullContent)
	if ret == 0 {
		return nil, fmt.Errorf("PrintWindow 失败")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(w), Height: -int32(h),
			Planes: 1, BitCount: 32, Compression: biRGB, SizeImage: uint32(w * h * 4),
		},
	}
	n, _, _ := procGetDIBits.Call(memDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	if n == 0 {
		return nil, fmt.Errorf("GetDIBits 失败")
	}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2] = img.Pix[i+2], img.Pix[i]
	}
	return img, nil
}

// isBlank 判断图像是否几乎全黑（PrintWindow 对某些硬件加速窗口会返回空帧）。
func isBlank(img *image.RGBA) bool {
	b := img.Bounds()
	if b.Empty() {
		return true
	}
	var sum, n int
	step := maxInt(1, b.Dx()/40)
	for y := b.Min.Y; y < b.Max.Y; y += step * 2 {
		for x := b.Min.X; x < b.Max.X; x += step {
			c := img.RGBAAt(x, y)
			sum += int(c.R) + int(c.G) + int(c.B)
			n++
		}
	}
	if n == 0 {
		return true
	}
	return float64(sum)/float64(n*3) < 8
}

// CaptureScreen 抓取整个虚拟主屏。
func CaptureScreen() (*image.RGBA, error) {
	w, h := ScreenSize()
	return CaptureRect(Rect{0, 0, int32(w), int32(h)})
}

// cropBlackBars 去掉 PPT 放映时上下（或左右）的黑边。
func cropBlackBars(img *image.RGBA) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 32 || h < 32 {
		return img
	}
	isDark := func(x, y int) bool {
		c := img.RGBAAt(x, y)
		return c.R < 26 && c.G < 26 && c.B < 26
	}
	// 判断整行/整列是否全黑，允许少量杂点。
	rowDark := func(y int) bool {
		dark, total := 0, 0
		for x := 0; x < w; x += 4 {
			total++
			if isDark(x, y) {
				dark++
			}
		}
		return total > 0 && dark*100/total >= 97
	}
	colDark := func(x int) bool {
		dark, total := 0, 0
		for y := 0; y < h; y += 4 {
			total++
			if isDark(x, y) {
				dark++
			}
		}
		return total > 0 && dark*100/total >= 97
	}
	top, bottom := 0, h-1
	for top < h/3 && rowDark(top) {
		top++
	}
	for bottom > h*2/3 && rowDark(bottom) {
		bottom--
	}
	left, right := 0, w-1
	for left < w/3 && colDark(left) {
		left++
	}
	for right > w*2/3 && colDark(right) {
		right--
	}
	if top >= bottom || left >= right {
		return img
	}
	// 保留一点边距，避免把阴影裁掉。
	const margin = 2
	top = maxInt(0, top-margin)
	left = maxInt(0, left-margin)
	bottom = minInt(h-1, bottom+margin)
	right = minInt(w-1, right+margin)
	out := image.NewRGBA(image.Rect(0, 0, right-left+1, bottom-top+1))
	draw.Draw(out, out.Bounds(), img, image.Pt(left, top), draw.Src)
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Resize 使用最近邻缩放，够用于缩略图与模型输入。
func Resize(src *image.RGBA, maxW int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxW <= 0 || w <= maxW {
		return src
	}
	ratio := float64(maxW) / float64(w)
	nw := maxW
	nh := int(float64(h) * ratio)
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := int(float64(y) / ratio)
		if sy >= h {
			sy = h - 1
		}
		for x := 0; x < nw; x++ {
			sx := int(float64(x) / ratio)
			if sx >= w {
				sx = w - 1
			}
			dst.SetRGBA(x, y, src.RGBAAt(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// DHash 计算 64 位差值哈希，用于判断画面是否变化。
func DHash(img *image.RGBA) uint64 {
	const w, h = 9, 8
	gray := make([]uint8, w*h)
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return 0
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := b.Min.X + x*sw/(w-1)
			sy := b.Min.Y + y*sh/(h-1)
			if sx >= b.Max.X {
				sx = b.Max.X - 1
			}
			if sy >= b.Max.Y {
				sy = b.Max.Y - 1
			}
			c := img.RGBAAt(sx, sy)
			gray[y*w+x] = uint8((299*uint32(c.R) + 587*uint32(c.G) + 114*uint32(c.B)) / 1000)
		}
	}
	var hash uint64
	for y := 0; y < h; y++ {
		for x := 0; x < w-1; x++ {
			hash <<= 1
			if gray[y*w+x] > gray[y*w+x+1] {
				hash |= 1
			}
		}
	}
	return hash
}

// Hamming 返回两个哈希的汉明距离。
func Hamming(a, b uint64) int {
	x := a ^ b
	n := 0
	for x != 0 {
		x &= x - 1
		n++
	}
	return n
}

// EncodePNG 编码为 PNG 字节。
func EncodePNG(img image.Image) ([]byte, error) {
	f, err := os.CreateTemp("", "cn-*.png")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(f, img); err != nil {
		return nil, err
	}
	return os.ReadFile(f.Name())
}

// SaveImage 保存图片到磁盘，自动创建目录。
func SaveImage(img image.Image, path string, quality int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if strings.EqualFold(filepath.Ext(path), ".jpg") || strings.EqualFold(filepath.Ext(path), ".jpeg") {
		if quality <= 0 || quality > 100 {
			quality = 88
		}
		return jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
	}
	return png.Encode(f, img)
}

// DominantBrightness 返回图像平均亮度，用于判断是否为空白页。
func DominantBrightness(img *image.RGBA) float64 {
	b := img.Bounds()
	if b.Empty() {
		return 0
	}
	var sum, n uint64
	step := maxInt(1, b.Dx()/64)
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			c := img.RGBAAt(x, y)
			sum += uint64(c.R) + uint64(c.G) + uint64(c.B)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(sum) / float64(n*3*255)
}

// AvgColor 返回图像平均颜色（忽略纯黑边）。
func AvgColor(img *image.RGBA) color.RGBA {
	b := img.Bounds()
	var r, g, bl, n uint64
	step := maxInt(1, b.Dx()/80)
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			c := img.RGBAAt(x, y)
			if c.R < 20 && c.G < 20 && c.B < 20 {
				continue
			}
			r += uint64(c.R)
			g += uint64(c.G)
			bl += uint64(c.B)
			n++
		}
	}
	if n == 0 {
		return color.RGBA{0, 0, 0, 255}
	}
	return color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255}
}

// SortByArea 按面积降序排序。
func SortByArea(ws []WindowInfo) {
	sort.Slice(ws, func(i, j int) bool {
		return ws[i].Rect.Width()*ws[i].Rect.Height() > ws[j].Rect.Width()*ws[j].Rect.Height()
	})
}

// DecodeFile 读取图片文件。
func DecodeFile(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), src, b.Min, draw.Src)
	return out, nil
}

// EncodeJPEG 编码为 JPEG 字节。
func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if quality <= 0 {
		quality = 88
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
