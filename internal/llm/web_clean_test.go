package llm

import (
	"strings"
	"testing"
)

func TestCleanReplyStripsFeedback(t *testing.T) {
	in := "通感是把不同感官的感觉打通的修辞。\n如回答不准确，帮助我们反馈\n复制\n"
	out := cleanReply(in, "")
	if strings.Contains(out, "反馈") {
		t.Fatalf("反馈文案未被清除: %q", out)
	}
	if strings.Contains(out, "复制") {
		t.Fatalf("按钮文案未被清除: %q", out)
	}
	if !strings.Contains(out, "通感") {
		t.Fatalf("正文被误删: %q", out)
	}
}

func TestCleanReplyDedupBlocks(t *testing.T) {
	body := "《荷塘月色》写于 1927 年。\n作者朱自清当时在清华大学任教。"
	in := body + "\n\n" + body
	out := cleanReply(in, "")
	if strings.Count(out, "荷塘月色") != 1 {
		t.Fatalf("重复段落未去重: %q", out)
	}
}

func TestCleanReplyDedupWholeReply(t *testing.T) {
	half := "文章结构分为四个部分：缘由、荷塘、月色、归来。每一部分都围绕作者的情绪变化展开，形成一条完整的心绪线索。"
	out := cleanReply(half+half, "")
	if strings.Count(out, "缘由") != 1 {
		t.Fatalf("整段重复未取半: %q", out)
	}
	if len([]rune(out)) > len([]rune(half))+8 {
		t.Fatalf("去重后仍然过长: %q", out)
	}
}

func TestCleanReplyFixesMojibake(t *testing.T) {
	// “你好通感”按 UTF-8 是 e4 bd a0 / e5 a5 bd / e9 80 9a / e6 84 9f，
	// 被按 CP1252 解码后的典型形态（每个字节变成一个西欧字符）。
	mojibake := "ä½\u00a0å¥½é€šæ„Ÿ"
	if !looksMojibake(mojibake) {
		t.Fatalf("未识别出乱码")
	}
	fixed := fixMojibake(mojibake)
	if !strings.Contains(fixed, "通感") || !strings.Contains(fixed, "你好") {
		t.Fatalf("乱码未修复: %q -> %q", mojibake, fixed)
	}
}

func TestCleanReplyKeepsNormal(t *testing.T) {
	in := "## 知识点\n- 通感：把听觉、视觉等感觉打通\n- 例句：微风过处，送来缕缕清香"
	out := cleanReply(in, "")
	if !strings.Contains(out, "通感") || !strings.Contains(out, "缕缕清香") {
		t.Fatalf("正文被误删: %q", out)
	}
}
