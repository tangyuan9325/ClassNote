package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	cls, _ := windows.UTF16PtrFromString("ClassNoteFloatBall")
	p := windows.NewLazySystemDLL("user32.dll").NewProc("FindWindowW")
	h, _, err := p.Call(uintptr(unsafe.Pointer(cls)), 0)
	fmt.Println("floatball hwnd:", h, err)
}
