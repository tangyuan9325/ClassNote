package lesson

import (
	"regexp"
	"strings"
)

// 本文件处理 ASR 幻觉。
//
// 实机日志与报告里最刺眼的问题：整堂课的转写几乎都是
// 「课堂讲课内容，语文、化学、物理等学科术语。」反复重复 13786 字。
// 这是 whisper 小模型在**静音或纯背景噪声**上的典型幻觉——它会输出训练语料里
// 高频的模板句，而不是识别失败。VAD 把教室底噪切成语段送进模型，就会稳定复现。
//
// 处理方式是保守的后处理：只删两类东西——已知幻觉短语，以及同一句话在一段里
// 反复出现 3 次以上的重复。真实讲课不会把一句话原样说三遍，所以误删风险很低。

// hallucinationPhrases 已知的 ASR 幻觉关键词（命中即判为噪声）。
// 这些都来自教学场景下的真实复现，不是猜测。
var hallucinationPhrases = []string{
	"课堂讲课内容",
	"学科术语",
	"请不吝",
	"点赞", "订阅", "打赏", "转发",
	"字幕由", "字幕提供", "本字幕",
	"amara.org",
	"明镜与点点",
	"谢谢观看", "感谢观看", "下集再见", "我们下期再见",
	"请开启wifi", "请开启wi-fi",
}

// 句子切分：保留标点，避免把整段当成一句。
var reSentence = regexp.MustCompile(`[^。！？；，、\n]+[。！？；，、]?`)

// cleanASRText 清洗一段转写文本，去掉幻觉与病态重复。
// 返回空字符串表示“这段音频没有有效内容”，调用方应丢弃而不是落库。
func cleanASRText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !needsASRClean(s) {
		return s
	}
	parts := reSentence.FindAllString(s, -1)
	if len(parts) == 0 {
		return s
	}
	// 先统计每个片段的出现次数。幻觉句被逗号切碎后会留下 "语文、" "化学、"
	// 这类短残渣，它们本身不含关键词，只能靠"同段高频重复"识别。
	counts := map[string]int{}
	for _, p := range parts {
		if k := fragmentKey(p); k != "" {
			counts[k]++
		}
	}
	seen := map[string]int{}
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		if isHallucination(t) {
			continue
		}
		key := fragmentKey(t)
		if len([]rune(key)) >= 6 {
			seen[key]++
			if seen[key] >= 3 {
				continue
			}
		} else if counts[key] >= 5 {
			// 极短片段重复 5 次以上，只可能来自幻觉或口癖，整段丢弃。
			continue
		}
		kept = append(kept, t)
	}
	out := strings.TrimSpace(strings.Join(kept, ""))
	// 用绝对长度判定“这段没内容”。
	// 不要用“保留率”判定：一份转写里可能 90% 是幻觉，剩下 10% 恰恰是老师
	// 讲的关键那句。按比例砍会把有效内容一起丢掉（语文课那份就这样被清成 0 字）。
	if len([]rune(out)) < 12 {
		return ""
	}
	return out
}

// needsASRClean 快速预判，避免每段都跑正则。
func needsASRClean(s string) bool {
	if len([]rune(s)) < 24 {
		return false
	}
	if strings.Contains(s, "课堂讲课内容") || strings.Contains(s, "学科术语") {
		return true
	}
	// 同一短语连续出现（"谢谢大家谢谢大家谢谢大家"）。
	if hasRunawayRepeat(s) {
		return true
	}
	return false
}

// hasRunawayRepeat 检测“某个 8 字以内的片段在 200 字里出现 5 次以上”。
func hasRunawayRepeat(s string) bool {
	r := []rune(s)
	if len(r) < 200 {
		return false
	}
	window := r[:200]
	for size := 4; size <= 8; size++ {
		counts := map[string]int{}
		for i := 0; i+size <= len(window); i++ {
			counts[string(window[i:i+size])]++
			if counts[string(window[i:i+size])] >= 5 {
				return true
			}
		}
	}
	return false
}

// fragmentKey 归一化片段用于重复计数。
func fragmentKey(s string) string {
	return strings.ToLower(strings.Trim(s, "。！？；，、 \n\t"))
}

func isHallucination(s string) bool {
	low := strings.ToLower(s)
	for _, p := range hallucinationPhrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// cleanTranscriptForReport 给历史数据兜底：老会话里已经存进了幻觉文本，
// 报告渲染时再清一遍（不修改落库内容）。
func cleanTranscriptForReport(s string) string {
	out := cleanASRText(s)
	if out == "" && strings.TrimSpace(s) != "" {
		return ""
	}
	return out
}
