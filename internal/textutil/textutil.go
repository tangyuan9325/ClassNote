// Package textutil 放一些中文文本的粗粒度比对工具。
package textutil

import (
	"strings"
	"unicode"
)

// Compact 去掉空白与 Markdown 标记，用于比较两段文字是否讲的是同一件事。
func Compact(s string) string {
	var b strings.Builder
	for _, r := range s {
		// 标点与符号一律丢弃：OCR 与 PPTX 的标点差异（：/、/·）不该影响
		// 「这两段是不是同一段话」的判断。
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// MostlyContained 判断 a 是否基本已经被 b 覆盖。
//
// 用「a 的每个二元组是否出现在 b 里」做粗判：中文没有空格，按词切分要额外
// 依赖，而二元组足够判断两段文字是不是同一段话，且对少量错字不敏感。
func MostlyContained(a, b string) bool {
	x := Compact(a)
	y := Compact(b)
	if x == "" {
		return true
	}
	if y == "" {
		return false
	}
	rx, ry := []rune(x), []rune(y)
	if len(rx) < 2 {
		return strings.Contains(y, x)
	}
	set := make(map[string]bool, len(ry))
	for i := 0; i < len(ry)-1; i++ {
		set[string(ry[i:i+2])] = true
	}
	hit := 0
	for i := 0; i < len(rx)-1; i++ {
		if set[string(rx[i:i+2])] {
			hit++
		}
	}
	return float64(hit)/float64(len(rx)-1) > 0.85
}
