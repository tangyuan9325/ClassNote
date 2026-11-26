//go:build !windows

package app

import (
	"fmt"

	"classnote/internal/hotkey"
)

// Hotkey 在非 Windows 平台是空壳，只为让跨平台编译与测试通过。
type Hotkey struct {
	Combo hotkey.Combo
}

// RegisterHotkey 在非 Windows 平台不可用。
func RegisterHotkey(combo string, fn func()) (*Hotkey, error) {
	return nil, fmt.Errorf("全局快捷键仅支持 Windows")
}

// Close 什么都不做。
func (h *Hotkey) Close() {}
