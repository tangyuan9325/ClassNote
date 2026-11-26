package hotkey

import "testing"

func TestParseCombo(t *testing.T) {
	c, err := ParseCombo("Ctrl+Alt+S")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if c.Mods != ModCtrl|ModAlt|modNoRepeat || c.Key != 'S' {
		t.Fatalf("结果不符: %+v", c)
	}
	if c.Text != "Ctrl+Alt+S" {
		t.Fatalf("规范写法不对: %s", c.Text)
	}
}

func TestParseComboLooseInput(t *testing.T) {
	cases := map[string]uint32{
		"ctrl + shift + f5": 0x74,
		"WIN+ALT+1":         '1',
		"Alt+PrintScreen":   0x2C,
		"Ctrl+空格":          0x20,
	}
	for in, want := range cases {
		c, err := ParseCombo(in)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", in, err)
		}
		if c.Key != want {
			t.Fatalf("%s 主键应为 0x%X，实际 0x%X", in, want, c.Key)
		}
	}
}

func TestParseComboRejects(t *testing.T) {
	bad := []string{"", "S", "Ctrl", "Ctrl+", "Ctrl+A+B", "Hyper+X", "Ctrl+F25"}
	for _, in := range bad {
		if _, err := ParseCombo(in); err == nil {
			t.Fatalf("%q 应当被拒绝", in)
		}
	}
}

func TestSameCombo(t *testing.T) {
	if !SameCombo("ctrl+alt+s", "Alt + Ctrl + S") {
		t.Fatal("等价写法应判为相同")
	}
	if SameCombo("Ctrl+Alt+S", "Ctrl+Shift+S") {
		t.Fatal("不同组合不应判为相同")
	}
}
