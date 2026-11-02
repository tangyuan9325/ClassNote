package lesson

// 照搬程度的统一刻度。
//
// 1.2.5 里同一套分数被解释了三遍，而且阈值各不相同：
//   - prompts.go 给模型的锚点是 80 / 55 / 25 四档；
//   - report.go 打标签用的是 75 / 50 / 25，且 Copying 标志另取 ≥60；
//   - engine.go 的兜底证据文案用 70 / 40，README 也照抄了 70 / 40。
//
// 结果是同样一个 68 分，在不同页面上被判成「部分照搬」和「疑似照搬」两种结论。
// 这里收敛成一份，改阈值只改这里。
const (
	// CopyRateHigh 及以上视为高度照搬。
	CopyRateHigh = 80
	// CopyRatePartial 及以上视为部分照搬。
	CopyRatePartial = 55
	// CopyRateExplain 及以上视为以讲解为主，低于则基本脱离画面。
	CopyRateExplain = 25
	// CopyFlagFrom 及以上在会话数据里标记 Copying=true。
	CopyFlagFrom = CopyRatePartial
)

// copyLabel 把 0-100 的照搬分翻译成结论词。
func copyLabel(rate int) string {
	switch {
	case rate < 0:
		return "无语音证据"
	case rate >= CopyRateHigh:
		return "高度照搬"
	case rate >= CopyRatePartial:
		return "部分照搬"
	case rate >= CopyRateExplain:
		return "以讲解为主"
	default:
		return "以自主讲解为主"
	}
}

// copyFallbackEvidence 在没有模型证据时，按分数给一句兜底说明。
func copyFallbackEvidence(score int) string {
	switch {
	case score >= CopyRateHigh:
		return "转写内容与画面文字高度重合，缺少自己的解释与举例"
	case score >= CopyRatePartial:
		return "大段复述画面文字，穿插了少量补充"
	default:
		return "以自主讲解为主，画面文字只作为提纲被提及"
	}
}
