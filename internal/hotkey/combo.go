package hotkey

import (
	"fmt"
	"strings"
)

// 修饰键位与 Windows RegisterHotKey 的 MOD_* 常量一致。
const (
	ModAlt      uint32 = 0x0001
	ModCtrl     uint32 = 0x0002
	ModShift    uint32 = 0x0004
	ModWin      uint32 = 0x0008
	modNoRepeat uint32 = 0x4000
)

// Combo 是一个解析后的快捷键。
type Combo struct {
	Mods uint32
	Key  uint32
	Text string // 规范化后的写法，如 Ctrl+Alt+S
}

// ParseCombo 解析 "Ctrl+Alt+S" 这类组合键。
//
// 拒绝没有修饰键的单键：RegisterHotKey 会把它全局吞掉，
// 用户按一下 S 就再也打不出字。
func ParseCombo(s string) (Combo, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Combo{}, fmt.Errorf("快捷键不能为空")
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '+' || r == ' ' })
	var mods uint32
	var key uint32
	var names []string
	seen := map[uint32]bool{}

	for _, p := range parts {
		if p == "" {
			continue
		}
		switch strings.ToLower(p) {
		case "ctrl", "control", "ctl":
			if !seen[ModCtrl] {
				mods |= ModCtrl
				seen[ModCtrl] = true
				names = append(names, "Ctrl")
			}
			continue
		case "alt", "option":
			if !seen[ModAlt] {
				mods |= ModAlt
				seen[ModAlt] = true
				names = append(names, "Alt")
			}
			continue
		case "shift":
			if !seen[ModShift] {
				mods |= ModShift
				seen[ModShift] = true
				names = append(names, "Shift")
			}
			continue
		case "win", "windows", "meta", "cmd", "super":
			if !seen[ModWin] {
				mods |= ModWin
				seen[ModWin] = true
				names = append(names, "Win")
			}
			continue
		}
		k, ok := virtualKey(p)
		if !ok {
			return Combo{}, fmt.Errorf("看不懂的按键 %q（支持 A-Z、0-9、F1-F24 与常用功能键）", p)
		}
		if key != 0 && key != k {
			return Combo{}, fmt.Errorf("只能有一个主键，%q 多余", p)
		}
		key = k
		names = append(names, strings.ToUpper(p))
	}
	if key == 0 {
		return Combo{}, fmt.Errorf("缺少主键，例如 Ctrl+Alt+S")
	}
	if mods == 0 {
		return Combo{}, fmt.Errorf("必须带 Ctrl / Alt / Shift / Win 中的至少一个，否则会占用普通按键")
	}
	return Combo{Mods: mods | modNoRepeat, Key: key, Text: strings.Join(names, "+")}, nil
}

// virtualKey 把按键名映射成 Windows 虚拟键码。
func virtualKey(p string) (uint32, bool) {
	u := strings.ToUpper(p)
	if len(u) == 1 {
		c := u[0]
		if c >= 'A' && c <= 'Z' {
			return uint32(c), true
		}
		if c >= '0' && c <= '9' {
			return uint32(c), true
		}
	}
	switch u {
	case "SPACE", "空格":
		return 0x20, true
	case "ENTER", "RETURN", "回车":
		return 0x0D, true
	case "TAB":
		return 0x09, true
	case "ESC", "ESCAPE":
		return 0x1B, true
	case "BACKSPACE":
		return 0x08, true
	case "INSERT", "INS":
		return 0x2D, true
	case "DELETE", "DEL":
		return 0x2E, true
	case "HOME":
		return 0x24, true
	case "END":
		return 0x23, true
	case "PAGEUP", "PGUP":
		return 0x21, true
	case "PAGEDOWN", "PGDN":
		return 0x22, true
	case "PRINTSCREEN", "PRTSC", "截图键":
		return 0x2C, true
	case "UP":
		return 0x26, true
	case "DOWN":
		return 0x28, true
	case "LEFT":
		return 0x25, true
	case "RIGHT":
		return 0x27, true
	}
	if strings.HasPrefix(u, "F") && len(u) <= 3 {
		var n int
		if _, err := fmt.Sscanf(u, "F%d", &n); err == nil && n >= 1 && n <= 24 {
			return uint32(0x70 + n - 1), true
		}
	}
	return 0, false
}

// SameCombo 比较两个写法是否等价（用于判断配置是否真的改了）。
func SameCombo(a, b string) bool {
	ca, ea := ParseCombo(a)
	cb, eb := ParseCombo(b)
	if ea != nil || eb != nil {
		return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	return ca.Mods == cb.Mods && ca.Key == cb.Key
}
