package asrtext

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

// reBigSentence 只按句末标点切，用于跨逗号的判断（学科枚举就横跨多个逗号）。
var reBigSentence = regexp.MustCompile(`[^。！？\n]+[。！？]?`)

// 学科枚举幻觉。
//
// 这是 prompt 回声的变体：早先转写请求里带了一句
// 「课堂讲课内容，语文、化学、物理等学科术语。」当提示词，whisper 把它当作
// 上文，在没有明确语音的段落上顺着往下编，于是转写里会凭空多出
// 「有关语文、数学、英语等学科的内容。」——不是任何人的原话。
//
// 形状很固定：三四个学科名并列 + 以「等学科/等科目/等内容」收尾 + 句子很短。
// 三个条件同时成立才判为幻觉，正常讲课里「语文、数学、英语等学科」这种
// 陈述句很少见，即使误删也只是丢半句。
var (
	reSubject     = regexp.MustCompile(`语文|数学|英语|化学|物理|生物|历史|地理|政治|科学|音乐|美术|体育`)
	reSubjectEnd  = regexp.MustCompile(`等(学科|科目|课程|内容|知识|领域|方面)`)
	reSubjectLead = regexp.MustCompile(`有关|关于|涉及|包括`)
)

// mayContainSubjectEnum 粗筛：这段文本里有没有可能出现学科枚举幻觉。
// 只做便宜的关键字判断，真正的裁决在 isSubjectEnumeration。
func mayContainSubjectEnum(s string) bool {
	if reSubjectEnd.MatchString(s) {
		return true
	}
	return reSubjectLead.MatchString(s) && countSubjects(s) >= 3
}

// countSubjects 统计一句话里出现了几个不同的学科名。
func countSubjects(s string) int {
	seen := map[string]bool{}
	for _, m := range reSubject.FindAllString(s, -1) {
		seen[m] = true
	}
	return len(seen)
}

// isSubjectEnumeration 判断一句是否是学科枚举式幻觉。
//
// 两条判据，都要求句子很短：
//   - 以「等学科／等科目／等内容」收尾，且至少两个学科（"有关语文，数学，英语等学科的内容"）
//   - 以「有关／关于／涉及／包括」开头，且至少三个学科（"有关语文，数学，英语的内容"）
//
// 只命中两个学科、又没有「等…」收尾的句子一律放过——「关于语文和数学的关系，
// 大家想一想」是老师真的会说的话，不能删。
func isSubjectEnumeration(s string) bool {
	if len([]rune(s)) > 48 {
		return false
	}
	n := countSubjects(s)
	if n < 2 {
		return false
	}
	if reSubjectEnd.MatchString(s) {
		return true
	}
	return reSubjectLead.MatchString(s) && n >= 3
}

// Clean 清洗一段转写文本，去掉幻觉与病态重复。
// 返回空字符串表示“这段音频没有有效内容”，调用方应丢弃而不是落库。
func Clean(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !needsASRClean(s) {
		return s
	}
	// 学科枚举要先处理，而且必须按句号切、不能按逗号切：
	// 「有关语文，数学，英语等学科的内容。」按逗号拆开后就只剩一个学科名，
	// 永远凑不齐两个，检测会全部漏掉。
	if mayContainSubjectEnum(s) {
		big := reBigSentence.FindAllString(s, -1)
		kept := make([]string, 0, len(big))
		for _, seg := range big {
			if isSubjectEnumeration(seg) {
				continue
			}
			kept = append(kept, seg)
		}
		s = strings.TrimSpace(strings.Join(kept, ""))
		if s == "" {
			return ""
		}
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
	// 学科枚举式幻觉常常只有十几个字（"有关语文，数学，英语等。"），
	// 所以这条必须放在长度门限之前判断，否则会被直接放过。
	if mayContainSubjectEnum(s) {
		return true
	}
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

// IsEchoOfTail 判断这一次的结果是不是上一段的原样复读。
//
// 连续两段静音/噪声被喂给 whisper 时，它会给出一模一样的句子；真实讲课
// 不会把一整句原样重复两遍，所以整段相同且够长就丢掉后一次。短句（"对，"
// "好，"）不管，那是正常口头语。
func IsEchoOfTail(full, next string) bool {
	next = strings.TrimSpace(next)
	if len([]rune(next)) < 6 {
		return false
	}
	// 只处理「一整句」的复读；半句残留不碰，避免切分误差把话吃掉。
	if !strings.HasSuffix(next, "。") && !strings.HasSuffix(next, "！") && !strings.HasSuffix(next, "？") {
		return false
	}
	return strings.HasSuffix(fragmentKey(full), fragmentKey(next))
}

// CleanForReport 给历史数据兜底：老会话里已经存进了幻觉文本，
// 报告渲染时再清一遍（不修改落库内容）。
func CleanForReport(s string) string {
	out := Clean(s)
	if out == "" && strings.TrimSpace(s) != "" {
		return ""
	}
	return out
}
