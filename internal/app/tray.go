package app

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"classnote/internal/logx"
)

//go:embed assets/tray.ico assets/tray-recording.ico
var trayAssets embed.FS

// 托盘图标 ID 与菜单项。
const (
	trayIconID   = 1
	trayCallback = 0x8000 + 1 // WM_APP + 1
	trayClass    = "ClassNoteTrayWnd"

	menuOpen   = 1001
	menuToggle = 1002
	menuReport = 1003
	menuData   = 1004
	menuConfig = 1006
	menuQuit   = 1005
)

// 窗口消息。
const (
	wmDestroy      = 0x0002
	wmClose        = 0x0010
	wmLButtonUp    = 0x0202
	wmLButtonDbl   = 0x0203
	wmRButtonUp    = 0x0205
	wmContextMenu  = 0x007B
	wmCommand      = 0x0111
	wmNull         = 0x0000
	wmApp          = 0x8000
	csVRedraw      = 0x0001
	csHRedraw      = 0x0002
	idiApplication = 32512
)

// Shell_NotifyIcon 相关。
const (
	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nimVersion = 4

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010
	nifShowTip = 0x00000080

	notifyIconVersion4 = 4

	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	smCxSmIcon     = 49
	smCySmIcon     = 50

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfGrayed    = 0x00000001
	mfDefault   = 0x00000000

	tpmLeftAlign   = 0x0000
	tpmBottomAlign = 0x0020
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	mbYesNo       = 0x00000004
	mbIconWarning = 0x00000030
	mbTopMost     = 0x00040000
	idYes         = 6
)

var (
	trayUser32  = windows.NewLazySystemDLL("user32.dll")
	trayShell32 = windows.NewLazySystemDLL("shell32.dll")
	trayKernel  = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassEx        = trayUser32.NewProc("RegisterClassExW")
	procCreateWindowEx         = trayUser32.NewProc("CreateWindowExW")
	procDefWindowProc          = trayUser32.NewProc("DefWindowProcW")
	procDestroyWindow          = trayUser32.NewProc("DestroyWindow")
	procGetMessage             = trayUser32.NewProc("GetMessageW")
	procTranslateMessage       = trayUser32.NewProc("TranslateMessage")
	procDispatchMessage        = trayUser32.NewProc("DispatchMessageW")
	procPostMessage            = trayUser32.NewProc("PostMessageW")
	procPostQuitMessage        = trayUser32.NewProc("PostQuitMessage")
	procLoadImage              = trayUser32.NewProc("LoadImageW")
	procCreatePopupMenu        = trayUser32.NewProc("CreatePopupMenu")
	procAppendMenu             = trayUser32.NewProc("AppendMenuW")
	procTrackPopupMenu         = trayUser32.NewProc("TrackPopupMenu")
	procDestroyMenu            = trayUser32.NewProc("DestroyMenu")
	procSetForegroundWindow    = trayUser32.NewProc("SetForegroundWindow")
	procGetCursorPos           = trayUser32.NewProc("GetCursorPos")
	procRegisterWindowMessage  = trayUser32.NewProc("RegisterWindowMessageW")
	procGetSystemMetrics       = trayUser32.NewProc("GetSystemMetrics")
	procDestroyIcon            = trayUser32.NewProc("DestroyIcon")
	procMessageBox             = trayUser32.NewProc("MessageBoxW")
	procFindWindow             = trayUser32.NewProc("FindWindowW")
	procEnumWindows            = trayUser32.NewProc("EnumWindows")
	procIsWindowVisible        = trayUser32.NewProc("IsWindowVisible")
	procGetWindowTextLength    = trayUser32.NewProc("GetWindowTextLengthW")
	procGetWindowText          = trayUser32.NewProc("GetWindowTextW")
	procGetWindowThreadProcess = trayUser32.NewProc("GetWindowThreadProcessId")
	procShellNotifyIcon        = trayShell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandle        = trayKernel.NewProc("GetModuleHandleW")
	procShowWindow             = trayUser32.NewProc("ShowWindow")
)

// TrayStatus 是托盘显示所需的状态快照。
type TrayStatus struct {
	Recording bool
	Subject   string
	Elapsed   int
	Hint      string
}

// TrayActions 是托盘菜单触发的动作。
type TrayActions struct {
	Open    func(panel string)
	Toggle  func() string // 返回给用户看的提示
	Reports func()
	Data    func()
	Quit    func()
	Status  func() TrayStatus
}

// Tray 是 Windows 通知区域图标。零值不可用，请用 NewTray。
type Tray struct {
	actions TrayActions
	iconDir string

	ready chan struct{}
	done  chan struct{}
	once  sync.Once

	mu      sync.Mutex
	hwnd    windows.Handle
	hIdle   windows.Handle
	hRec    windows.Handle
	curIcon windows.Handle
	tip     string
	closed  bool

	taskbarCreated uint32
}

type winPoint struct{ X, Y int32 }

type winMsg struct {
	HWnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       winPoint
	LPrivate uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

var (
	trayWndProc = syscall.NewCallback(trayWindowProc)
	trayInst    *Tray // 供 WndProc 回查（同一进程只会有一个托盘）
)

// NewTray 创建托盘对象，图标会从内嵌资源释放到 iconDir。
func NewTray(iconDir string, actions TrayActions) (*Tray, error) {
	t := &Tray{
		actions: actions,
		iconDir: iconDir,
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
	}
	if err := t.writeIcons(); err != nil {
		return nil, err
	}
	return t, nil
}

// writeIcons 把内嵌图标写到数据目录，LoadImage 需要真实文件路径。
func (t *Tray) writeIcons() error {
	if err := os.MkdirAll(t.iconDir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"tray.ico", "tray-recording.ico"} {
		raw, err := trayAssets.ReadFile("assets/" + name)
		if err != nil {
			return err
		}
		dst := filepath.Join(t.iconDir, name)
		if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(raw)) {
			continue
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Start 启动消息循环（异步），并等待图标出现在通知区域。
func (t *Tray) Start() error {
	trayInst = t
	go t.run()
	select {
	case <-t.ready:
		return nil
	case <-t.done:
		return fmt.Errorf("托盘初始化失败")
	}
}

func (t *Tray) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)

	hInst, _, _ := procGetModuleHandle.Call(0)
	className, _ := windows.UTF16PtrFromString(trayClass)

	wc := wndClassEx{
		Style:      csHRedraw | csVRedraw,
		WndProc:    trayWndProc,
		Instance:   windows.Handle(hInst),
		ClassName:  className,
		Background: 0,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		logx.Warnf("tray", "注册托盘窗口类失败: %v", err)
		return
	}

	title, _ := windows.UTF16PtrFromString("ClassNote")
	// WS_POPUP 且不显示，只用来收消息；不会出现在任务栏。
	hwnd, _, err := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)), 0x80000000, 0, 0, 0, 0,
		0, 0, hInst, 0)
	if hwnd == 0 {
		logx.Warnf("tray", "创建托盘窗口失败: %v", err)
		return
	}
	t.mu.Lock()
	t.hwnd = windows.Handle(hwnd)
	t.mu.Unlock()

	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	if r, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(name))); r != 0 {
		t.taskbarCreated = uint32(r)
	}

	t.loadIcons()
	if !t.addIcon() {
		logx.Warnf("tray", "添加托盘图标失败")
		return
	}
	close(t.ready)

	var m winMsg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	logx.Infof("tray", "托盘已退出")
}

func (t *Tray) loadIcons() {
	cx, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCySmIcon)
	if cx == 0 {
		cx = 16
	}
	if cy == 0 {
		cy = 16
	}
	load := func(name string) windows.Handle {
		p, err := windows.UTF16PtrFromString(filepath.Join(t.iconDir, name))
		if err != nil {
			return 0
		}
		h, _, _ := procLoadImage.Call(0, uintptr(unsafe.Pointer(p)), imageIcon,
			cx, cy, lrLoadFromFile)
		if h == 0 {
			h, _, _ = procLoadImage.Call(0, uintptr(unsafe.Pointer(p)), imageIcon, 32, 32, lrLoadFromFile)
		}
		return windows.Handle(h)
	}
	t.mu.Lock()
	t.hIdle = load("tray.ico")
	t.hRec = load("tray-recording.ico")
	t.mu.Unlock()
	if t.hIdle == 0 {
		if h, _, _ := procLoadImage.Call(0, uintptr(idiApplication), imageIcon, 0, 0, 0); h != 0 {
			t.hIdle = windows.Handle(h)
		}
	}
	if t.hRec == 0 {
		t.hRec = t.hIdle
	}
}

func (t *Tray) notifyData(flags uint32) notifyIconData {
	n := notifyIconData{
		HWnd:             t.hwnd,
		UID:              trayIconID,
		UFlags:           flags,
		UCallbackMessage: trayCallback,
		HIcon:            t.curIcon,
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
	}
	copyUTF16(n.SzTip[:], t.tip)
	return n
}

func (t *Tray) addIcon() bool {
	t.mu.Lock()
	if t.hwnd == 0 {
		t.mu.Unlock()
		return false
	}
	if t.curIcon == 0 {
		t.curIcon = t.hIdle
	}
	if t.tip == "" {
		t.tip = "课记 · 课堂笔记助手"
	}
	hwnd := t.hwnd
	n := t.notifyData(nifMessage | nifIcon | nifTip | nifShowTip)
	t.mu.Unlock()

	r, _, _ := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return false
	}
	// 版本 4 让左键单击也能收到 WM_LBUTTONUP。
	ver := n
	ver.UFlags = 0
	ver.UVersion = notifyIconVersion4
	procShellNotifyIcon.Call(nimVersion, uintptr(unsafe.Pointer(&ver)))
	t.mu.Lock()
	t.hwnd = hwnd
	t.mu.Unlock()
	t.SetStatus(TrayStatus{})
	return true
}

// SetStatus 更新图标、提示文字与菜单状态。
func (t *Tray) SetStatus(st TrayStatus) {
	t.mu.Lock()
	hwnd := t.hwnd
	if hwnd == 0 || t.closed {
		t.mu.Unlock()
		return
	}
	icon := t.hIdle
	tip := "课记 · 空闲"
	if st.Recording {
		icon = t.hRec
		tip = "课记 · 记录中"
		if st.Subject != "" {
			tip += " · " + st.Subject
		}
		if st.Elapsed > 0 {
			tip += " " + clock(st.Elapsed)
		}
	}
	if st.Hint != "" {
		tip = st.Hint
	}
	if icon == 0 {
		icon = t.hIdle
	}
	t.curIcon = icon
	t.tip = tip
	n := t.notifyData(nifIcon | nifTip | nifShowTip)
	t.mu.Unlock()

	r, _, _ := procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		// 图标丢了（Explorer 重启、被系统回收）：静默补挂，避免老师看不到托盘。
		if t.addIcon() {
			logx.Infof("tray", "托盘图标已重新挂载")
		} else {
			logx.Debugf("tray", "托盘图标补挂失败，下次再试")
		}
	}
}

// Notify 弹出一条气泡提示（窗口可能被遮挡时使用）。
func (t *Tray) Notify(title, msg string) {
	t.mu.Lock()
	hwnd := t.hwnd
	if hwnd == 0 || t.closed {
		t.mu.Unlock()
		return
	}
	n := t.notifyData(nifInfo | nifIcon | nifTip | nifShowTip)
	copyUTF16(n.SzInfoTitle[:], title)
	copyUTF16(n.SzInfo[:], msg)
	n.DwInfoFlags = 0x00000001 // NIIF_INFO
	t.mu.Unlock()
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

// Stop 移除图标并结束消息循环。
func (t *Tray) Stop() {
	t.once.Do(func() {
		t.mu.Lock()
		hwnd := t.hwnd
		t.closed = true
		t.mu.Unlock()
		if hwnd != 0 {
			n := notifyIconData{
				HWnd:   hwnd,
				UID:    trayIconID,
				CbSize: uint32(unsafe.Sizeof(notifyIconData{})),
			}
			procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&n)))
			procPostMessage.Call(uintptr(hwnd), wmClose, 0, 0)
		}
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
		}
		t.mu.Lock()
		if t.hIdle != 0 {
			procDestroyIcon.Call(uintptr(t.hIdle))
		}
		if t.hRec != 0 && t.hRec != t.hIdle {
			procDestroyIcon.Call(uintptr(t.hRec))
		}
		t.hIdle, t.hRec = 0, 0
		t.mu.Unlock()
	})
}

// showMenu 在鼠标位置弹出菜单。
func (t *Tray) showMenu() {
	logx.Infof("tray", "菜单请求")
	if t.actions.Status == nil {
		logx.Warnf("tray", "菜单缺少状态回调")
		return
	}
	st := t.actions.Status()

	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		logx.Warnf("tray", "创建菜单失败")
		return
	}
	defer procDestroyMenu.Call(menu)

	appendItem(menu, mfString|mfGrayed, 0, statusLine(st))
	appendItem(menu, mfSeparator, 0, "")
	appendItem(menu, mfString|mfDefault, menuOpen, "打开主界面")
	toggleText := "开始记录"
	if st.Recording {
		toggleText = "结束并生成报告"
	}
	appendItem(menu, mfString, menuToggle, toggleText)
	appendItem(menu, mfString, menuConfig, "设置…")
	appendItem(menu, mfSeparator, 0, "")
	appendItem(menu, mfString, menuReport, "打开报告目录")
	appendItem(menu, mfString, menuData, "打开数据目录")
	appendItem(menu, mfSeparator, 0, "")
	appendItem(menu, mfString, menuQuit, "退出课记")

	var pt winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	// 不先置前，点菜单外部时菜单不会消失。
	procSetForegroundWindow.Call(uintptr(hwnd))
	logx.Infof("tray", "弹出菜单 hwnd=%v cursor=%d,%d", hwnd, pt.X, pt.Y)
	cmd, _, _ := procTrackPopupMenu.Call(menu,
		tpmReturnCmd|tpmRightButton|tpmLeftAlign|tpmBottomAlign,
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)
	procPostMessage.Call(uintptr(hwnd), wmNull, 0, 0)

	logx.Infof("tray", "菜单选择 %d", cmd)
	t.RunCommand(uint32(cmd))
}

// RunCommand 执行托盘菜单命令。菜单点击与外部触发（自动测试、快捷方式）共用这条路径。
func (t *Tray) RunCommand(id uint32) {
	switch id {
	case menuOpen:
		t.invoke(func() { t.actions.Open("") })
	case menuToggle:
		t.invoke(func() {
			if t.actions.Toggle == nil {
				return
			}
			msg := t.actions.Toggle()
			if msg != "" {
				t.Notify("课记", msg)
			}
		})
	case menuConfig:
		t.invoke(func() { t.actions.Open("settings") })
	case menuReport:
		t.invoke(func() { t.actions.Reports() })
	case menuData:
		t.invoke(func() { t.actions.Data() })
	case menuQuit:
		t.invoke(func() { t.actions.Quit() })
	}
}

// invoke 把可能阻塞的动作放到独立协程，避免卡住托盘消息循环。
func (t *Tray) invoke(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logx.Warnf("tray", "托盘动作异常: %v", r)
			}
		}()
		fn()
	}()
}

func (t *Tray) onClick() {
	if t.actions.Open != nil {
		t.invoke(func() { t.actions.Open("") })
	}
}

func statusLine(st TrayStatus) string {
	if !st.Recording {
		if st.Hint != "" {
			return st.Hint
		}
		return "当前空闲"
	}
	s := "正在记录"
	if st.Subject != "" {
		s += " · " + st.Subject
	}
	if st.Elapsed > 0 {
		s += "  " + clock(st.Elapsed)
	}
	return s
}

func appendItem(menu uintptr, flags uintptr, id uintptr, text string) {
	if text == "" {
		procAppendMenu.Call(menu, mfSeparator, 0, 0)
		return
	}
	p, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	procAppendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(p)))
}

func copyUTF16(dst []uint16, s string) {
	if len(dst) == 0 {
		return
	}
	u, err := windows.UTF16FromString(s)
	if err != nil {
		dst[0] = 0
		return
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func clock(sec int) string {
	if sec < 0 {
		sec = 0
	}
	h, m, s := sec/3600, (sec%3600)/60, sec%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// trayWindowProc 是托盘窗口的消息处理函数。
func trayWindowProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	t := trayInst
	switch uint32(msg) {
	case trayCallback:
		if t == nil {
			return 0
		}
		// NOTIFYICON_VERSION_4 把事件放在 lParam 低位。
		ev := uint32(lparam) & 0xFFFF
		switch ev {
		case wmLButtonUp, wmLButtonDbl:
			t.onClick()
		case wmRButtonUp, wmContextMenu:
			// 必须在窗口所在线程弹出菜单：TrackPopupMenu 会开一个模态消息循环，
			// 换线程调用会导致菜单一闪而过甚至不显示。
			t.showMenu()
		}
		return 0
	case wmCommand:
		if t != nil {
			t.RunCommand(uint32(wparam) & 0xFFFF)
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	if t != nil && t.taskbarCreated != 0 && uint32(msg) == t.taskbarCreated {
		// 资源管理器重启后需要重新添加图标。
		go func() {
			t.addIcon()
			t.SetStatus(t.currentStatus())
		}()
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}

func (t *Tray) currentStatus() TrayStatus {
	if t.actions.Status == nil {
		return TrayStatus{}
	}
	return t.actions.Status()
}

// FindWindowByTitle 查找标题包含给定文字的可见顶层窗口。
func FindWindowByTitle(contains string) (windows.Handle, bool) {
	var found windows.Handle
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
			return 1
		}
		n, _, _ := procGetWindowTextLength.Call(hwnd)
		if n == 0 {
			return 1
		}
		buf := make([]uint16, n+1)
		procGetWindowText.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if syscall.UTF16ToString(buf) == contains {
			found = windows.Handle(hwnd)
			return 0
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found, found != 0
}

// FocusWindow 把窗口置前；窗口最小化时先还原。
func FocusWindow(h windows.Handle) {
	const swRestore = 9
	procShowWindow.Call(uintptr(h), swRestore)
	procSetForegroundWindow.Call(uintptr(h))
}

// AskQuit 在记录中退出前征求确认。
func AskQuit(hwnd windows.Handle, recording bool) bool {
	text := "确定要退出课记吗？"
	if recording {
		text = "正在记录课堂。退出会先结束本堂课并保存已产生的笔记。\n\n确定退出吗？"
	}
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("课记 ClassNote")
	r, _, _ := procMessageBox.Call(uintptr(hwnd), uintptr(unsafe.Pointer(t)),
		uintptr(unsafe.Pointer(c)), mbYesNo|mbIconWarning|mbTopMost)
	return int32(r) == idYes
}
