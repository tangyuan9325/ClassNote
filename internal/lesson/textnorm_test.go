package lesson

import (
	"strings"
	"testing"
)

func TestNormalizeMathLetters(t *testing.T) {
	// “𝐾𝑎2(𝐻2𝐶𝑂3)” 这类数学斜体
	in := "𝐾𝑎2(𝐻2𝐶𝑂3)"
	out := normalizeModelText(in)
	if strings.Contains(out, "𝐾") {
		t.Fatalf("数学字母未还原: %q", out)
	}
	if !strings.Contains(out, "Ka2") || !strings.Contains(out, "H2CO3") {
		t.Fatalf("还原结果不对: %q", out)
	}
}

func TestNormalizeArrowsAndSplitLines(t *testing.T) {
	// “𝑟𝑖𝑔ℎ𝑡𝑎𝑟𝑜𝑤”被逐字换行 + 单词残留
	in := "平衡②正移\n𝑟\n𝑖\n𝑔\nℎ\n𝑡\n𝑎\n𝑟\n𝑟\n𝑜\n𝑤\n析出沉淀"
	out := normalizeModelText(in)
	if strings.Contains(out, "rightarrow") || strings.Contains(out, "𝑟") {
		t.Fatalf("箭头未修复: %q", out)
	}
	if !strings.Contains(out, "→") {
		t.Fatalf("缺少箭头: %q", out)
	}
	if strings.Count(out, "\n") > 3 {
		t.Fatalf("逐字换行未合并: %q", out)
	}
}

func TestNormalizeKeepsChinese(t *testing.T) {
	in := "## 习题核对\n- 选择题答案：1-8 题依次为 A, B, A, D。\n- 升温会降低 CO2 溶解度。"
	out := normalizeModelText(in)
	if !strings.Contains(out, "习题核对") || !strings.Contains(out, "溶解度") {
		t.Fatalf("正常文本被破坏: %q", out)
	}
}

func TestNormalizeRightArrowTypo(t *testing.T) {
	// 实机数据里模型把 rightarrow 渲染成 rightarow（少一个 r）+ 散字
	in := "溶解度减小\n𝑟\n𝑖\n𝑔\nℎ\n𝑡\n𝑎\n𝑟\n𝑜\n𝑤\nrightarrow 平衡正移"
	out := normalizeModelText(in)
	if strings.Contains(out, "arow") {
		t.Fatalf("箭头残留: %q", out)
	}
	if !strings.Contains(out, "→") {
		t.Fatalf("缺少箭头: %q", out)
	}
}

func TestDedupeFormulaEcho(t *testing.T) {
	in := "涉及 Ka2(H2CO3)Ka2 \t (H2 \t CO3 \t ) 的综合表达"
	out := normalizeModelText(in)
	if strings.Count(out, "Ka2") != 1 {
		t.Fatalf("公式回声未去掉: %q", out)
	}
	if !strings.Contains(out, "综合表达") {
		t.Fatalf("正文被误删: %q", out)
	}
}

func TestLooksLikeRawJSON(t *testing.T) {
	raw := "{\n\"board_text\": \"x\",\n\"summary\": \"y\",\n\"notes\": \"z\"\n}"
	if !looksLikeRawJSON(raw) {
		t.Fatalf("未识别出原始 JSON")
	}
	if looksLikeRawJSON("## 知识点\n- 通感") {
		t.Fatalf("正常笔记被误判")
	}
}
