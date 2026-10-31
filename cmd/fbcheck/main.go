package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type rect struct {
	Left, Top, Right, Bottom int32
}

func main() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	enum := user32.NewProc("EnumWindows")
	getRect := user32.NewProc("GetWindowRect")
	getMetric := user32.NewProc("GetSystemMetrics")
	isVisible := user32.NewProc("IsWindowVisible")
	getClass := user32.NewProc("GetClassNameW")
	getPID := user32.NewProc("GetWindowThreadProcessId")
	setCursor := user32.NewProc("SetCursorPos")
	// 进程默认 DPI 不感知，GetWindowRect 会被缩放成 43x43；先声明 DPI 感知。
	user32.NewProc("SetProcessDPIAware").Call()

	sw, _, _ := getMetric.Call(0)
	sh, _, _ := getMetric.Call(1)
	fmt.Printf("screen=%dx%d\n", sw, sh)

	cb := syscall.NewCallback(func(h uintptr, l uintptr) uintptr {
		var buf [128]uint16
		n, _, _ := getClass.Call(h, uintptr(unsafe.Pointer(&buf[0])), 128)
		if n == 0 {
			return 1
		}
		cls := windows.UTF16ToString(buf[:n])
		if cls != "ClassNoteFloatBall" {
			return 1
		}
		var r rect
		getRect.Call(h, uintptr(unsafe.Pointer(&r)))
		vis, _, _ := isVisible.Call(h)
		var pid uint32
		getPID.Call(h, uintptr(unsafe.Pointer(&pid)))
		fmt.Printf("hwnd=%d pid=%d visible=%d rect=%d,%d,%d,%d size=%dx%d\n",
			h, pid, vis, r.Left, r.Top, r.Right, r.Bottom, r.Right-r.Left, r.Bottom-r.Top)
		if len(os.Args) > 1 && os.Args[1] == "hover" {
			setCursor.Call(uintptr(r.Left+(r.Right-r.Left)/2), uintptr(r.Top+(r.Bottom-r.Top)/2))
			fmt.Println("cursor moved to float ball")
		}
		return 1
	})
	enum.Call(cb, 0)

	// EnumWindows 偶尔拿不到（例如窗口在另一个 desktop），再用 FindWindow 兜底。
	cls, _ := windows.UTF16PtrFromString("ClassNoteFloatBall")
	if h, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(cls)), 0); h != 0 {
		var r rect
		getRect.Call(h, uintptr(unsafe.Pointer(&r)))
		fmt.Printf("findwindow hwnd=%d rect=%d,%d,%d,%d size=%dx%d\n",
			h, r.Left, r.Top, r.Right, r.Bottom, r.Right-r.Left, r.Bottom-r.Top)
	}
}
