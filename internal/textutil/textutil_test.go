package textutil

import "testing"

func TestMostlyContained(t *testing.T) {
	ppt := "## 三、导数与微分\n\n- 导数的定义：f'(x) = lim (f(x+Δx)-f(x))/Δx\n- 几何意义：切线斜率"
	if !MostlyContained("导数的定义 几何意义 切线斜率", ppt) {
		t.Fatal("明显被覆盖的文本应判为已包含")
	}
	if MostlyContained("课堂练习：求下列函数的导数", ppt) {
		t.Fatal("新内容不应判为已包含")
	}
	if !MostlyContained("", ppt) {
		t.Fatal("空串应视为已包含")
	}
	if MostlyContained("任意内容", "") {
		t.Fatal("基准为空时不可能是已包含（除非 OCR 也为空）")
	}
}

func TestCompact(t *testing.T) {
	if got := Compact(" ## 标题 \n - **要点** "); got != "标题要点" {
		t.Fatalf("清洗结果不符: %q", got)
	}
}
