package asrtext

import (
	"strings"
	"testing"
)

func TestDropsSubjectEnumeration(t *testing.T) {
	// 实机上抓到的原样：整段只有这一句，没有人真的说过它
	cases := []string{
		"有关语文，数学，英语等学科的内容。",
		"有关语文、数学、英语等学科的内容。",
		"课堂讲课内容，语文、化学、物理等学科术语。",
		"关于语文，数学，英语等科目的介绍。",
		"有关语文，数学，英语的内容。",
	}
	for _, c := range cases {
		if got := Clean(c); got != "" {
			t.Errorf("幻觉句未清除 %q -> %q", c, got)
		}
	}
}

func TestKeepsTextAroundSubjectEnumeration(t *testing.T) {
	// 幻觉句夹在真话后面：只删那句，后面的内容要留住
	in := "好，我们翻到第三页。有关语文、数学、英语等学科的内容。大家看这个公式。"
	got := Clean(in)
	if strings.Contains(got, "等学科") {
		t.Fatalf("学科枚举没清掉: %q", got)
	}
	if !strings.Contains(got, "翻到第三页") || !strings.Contains(got, "这个公式") {
		t.Fatalf("真话被误删: %q", got)
	}
}

func TestKeepsNormalLessonText(t *testing.T) {
	cases := []string{
		"今天这节课我们讲二次函数的顶点式。",
		"语文作业是背《岳阳楼记》第三段。",
		"同学们把书翻到第五十八页。",
		"这个结论在数学上叫零点定理。",
		"关于语文和数学的关系，大家想一想。",
	}
	for _, c := range cases {
		if got := Clean(c); got != c {
			t.Errorf("正常文本被改动 %q -> %q", c, got)
		}
	}
}

func TestDropsRepeatedTemplate(t *testing.T) {
	// 老版本整堂课都是这一句，重复到几千字
	in := strings.Repeat("课堂讲课内容，语文、化学、物理等学科术语。", 40)
	if got := Clean(in); got != "" {
		t.Fatalf("模板句复读没清掉，剩 %d 字", len([]rune(got)))
	}
}

func TestDropsCaptionOnly(t *testing.T) {
	// 静音/噪声段上 whisper 稳定吐出的旁白（实测于 whisper.cpp 1.7.4 + base 模型）
	cases := []string{"(把水煮到水上)", "（笑声）", "[音乐]", "【掌声】", "(用手指打開)"}
	for _, c := range cases {
		if got := Clean(c); got != "" {
			t.Errorf("旁白未被清除 %q -> %q", c, got)
		}
	}
	// 括号只是句中一部分的，不能删
	keep := []string{
		"我们把 a+b 写在括号（a+b）里。",
		"（a+b）的平方展开有三项。",
		"这一段（第二段）讲的是顶点式。",
	}
	for _, c := range keep {
		if got := Clean(c); got != c {
			t.Errorf("正常文本被误删 %q -> %q", c, got)
		}
	}
}

func TestEchoOfTail(t *testing.T) {
	full := "我们看这道题。好，翻到下一页。"
	if !IsEchoOfTail(full, "好，翻到下一页。") {
		t.Error("同句复读应该被识别")
	}
	if IsEchoOfTail(full, "我们看这道题。") {
		t.Error("前面出现过的句子不算尾巴复读")
	}
	// 短口头语重复是正常的，不能拦
	if IsEchoOfTail("对，对。", "对。") {
		t.Error("短句重复不该被当成幻觉")
	}
}
