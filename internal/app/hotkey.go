//go:build windows

package app

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"classnote/internal/hotkey"

	"classnote/internal/logx"
)

// 全局热键：一个独立线程注册 + 自建消息循环。
//
// RegisterHotKey 绑的是「注册它的那个线程」，所以扫描消息也必须在同一个线程；
// 放到别的 goroutine 里会永远收不到 WM_HOTKEY。
var (
	hkUser32      = windows.NewLazySystemDLL("user32.dll")
	procRegHotKey = hkUser32.NewProc("RegisterHotKey")
	procUnregHot  = hkUser32.NewProc("UnregisterHotKey")
	procGetMsg    = hkUser32.NewProc("GetMessageW")
	procPostMsg   = hkUser32.NewProc("PostThreadMessageW")
)

const (
	wmHotkey  = 0x0312
	wmQuit    = 0x0012
	hotkeyID  = 1
	errHotkey = 1409 // ERROR_HOTKEY_ALREADY_REGISTERED
)

type hkMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

// Hotkey 是一个已注册的全局快捷键。
type Hotkey struct {
	Combo    hotkey.Combo
	threadID uint32
	stop     chan struct{}
	once     sync.Once
}

// RegisterHotkey 注册全局组合键，按下时调用 fn（在独立 goroutine 里执行）。
func RegisterHotkey(combo string, fn func()) (*Hotkey, error) {
	c, err := hotkey.ParseCombo(combo)
	if err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	tidCh := make(chan uint32, 1)
	h := &Hotkey{Combo: c, stop: make(chan struct{})}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		tidCh <- windows.GetCurrentThreadId()

		r, _, e := procRegHotKey.Call(0, hotkeyID, uintptr(c.Mods), uintptr(c.Key))
		if r == 0 {
			code := uintptr(0)
			if errno, ok := e.(syscall.Errno); ok {
				code = uintptr(errno)
			}
			if code == errHotkey {
				ready <- fmt.Errorf("快捷键 %s 已被其他程序占用，换一个组合", c.Text)
			} else {
				ready <- fmt.Errorf("注册快捷键 %s 失败（错误码 %d）", c.Text, code)
			}
			return
		}
		ready <- nil

		var m hkMsg
		for {
			ret, _, _ := procGetMsg.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(ret) <= 0 { // WM_QUIT 或出错
				break
			}
			if m.message == wmHotkey {
				go func() {
					defer func() {
						if p := recover(); p != nil {
							logx.Errorf("hotkey", "快捷键回调崩溃: %v", p)
						}
					}()
					fn()
				}()
			}
		}
		procUnregHot.Call(0, hotkeyID)
		close(h.stop)
	}()

	if err := <-ready; err != nil {
		return nil, err
	}
	h.threadID = <-tidCh
	return h, nil
}

// Close 注销快捷键。可以重复调用。
func (h *Hotkey) Close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		if h.threadID != 0 {
			procPostMsg.Call(uintptr(h.threadID), wmQuit, 0, 0)
		}
		select {
		case <-h.stop:
		case <-time.After(1500 * time.Millisecond):
			logx.Warnf("hotkey", "快捷键线程未在 1.5 秒内退出")
		}
	})
}
