package main

import (
	"fmt"
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
		return 1
	})
	enum.Call(cb, 0)
}
