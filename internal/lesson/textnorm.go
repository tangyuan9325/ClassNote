package lesson

import (
	"regexp"
	"strings"
)

// normalizeModelText 清理模型输出里的“乱”：
//  1. 数学字母（𝐾𝑎2、𝑟𝑖𝑔ℎ𝑡𝑎𝑟𝑜𝑤 这类 Unicode 数学变体）还原为普通字符；
//  2. 被拆成单字符的公式行合并回去；
//  3. LaTeX 命令还原成可读符号（\frac{a}{b} → (a)/(b)，\sqrt{} → √，^\circ → °）；
//  4. 清掉零宽字符、KaTeX 残留。
//
// 背景：网页版模型常把 LaTeX 公式渲染成 Unicode 数学斜体，抓下来后每个字母独占一行，
// 在报告里看起来就是一堆乱码。
func normalizeModelText(s string) string {
	if s == "" {
		return s
	}
	s = stripZeroWidth(s)
	s = mapMathLetters(s)
	// 必须先合并被拆开的公式行，"rightarrow" 这类命令才能整体匹配上。
	s = collapseSplitMath(s)
	s = fixMathWords(s)
	return strings.TrimSpace(s)
}

// normalizeAll 批量清洗。
func normalizeAll(items ...string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = normalizeModelText(it)
	}
	return out
}

var zeroWidth = strings.NewReplacer(
	"\u200b", "", // 零宽空格
	"\u200c", "", "\u200d", "",
	"\ufeff", "", "\u2060", "",
)

func stripZeroWidth(s string) string { return zeroWidth.Replace(s) }

// mapMathLetters 把 Unicode 数学变体字母映射回 ASCII。
func mapMathLetters(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if a, ok := mathLetter(r); ok {
			b.WriteRune(a)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mathLetter 数学字母块：每个块 26 个字母（大写或小写），偶有缺字（如 ℎ）。
func mathLetter(r rune) (rune, bool) {
	// 字母似符号（U+2100-U+214F）里散落的几个
	switch r {
	case 'ℂ':
		return 'C', true
	case 'ℍ':
		return 'H', true
	case 'ℎ':
		return 'h', true
	case 'ℕ':
		return 'N', true
	case 'ℙ':
		return 'P', true
	case 'ℚ':
		return 'Q', true
	case 'ℝ':
		return 'R', true
	case 'ℤ':
		return 'Z', true
	case 'ℬ':
		return 'B', true
	case 'ℰ':
		return 'E', true
	case 'ℱ':
		return 'F', true
	case 'ℐ':
		return 'I', true
	case 'ℒ':
		return 'L', true
	case 'ℳ':
		return 'M', true
	case 'ℴ':
		return 'o', true
	}
	// 数学字母数字符号区 U+1D400-U+1D7FF
	if r < 0x1D400 || r > 0x1D7FF {
		return 0, false
	}
	blocks := []struct {
		start rune
		base  rune
		n     rune
	}{
		{0x1D400, 'A', 26}, {0x1D41A, 'a', 26},
		{0x1D434, 'A', 26}, {0x1D44E, 'a', 26},
		{0x1D468, 'A', 26}, {0x1D482, 'a', 26},
		{0x1D49C, 'A', 26}, {0x1D4B6, 'a', 26},
		{0x1D4D0, 'A', 26}, {0x1D4EA, 'a', 26},
		{0x1D504, 'A', 26}, {0x1D51E, 'a', 26},
		{0x1D538, 'A', 26}, {0x1D552, 'a', 26},
		{0x1D56C, 'A', 26}, {0x1D586, 'a', 26},
		{0x1D5A0, 'A', 26}, {0x1D5BA, 'a', 26},
		{0x1D5D4, 'A', 26}, {0x1D5EE, 'a', 26},
		{0x1D608, 'A', 26}, {0x1D622, 'a', 26},
		{0x1D63C, 'A', 26}, {0x1D656, 'a', 26},
		{0x1D670, 'A', 26}, {0x1D68A, 'a', 26},
		{0x1D6A8, 'A', 25}, {0x1D6C2, 'a', 25}, // 斜体（h 缺，用 U+210E）
		{0x1D6E2, 'A', 25}, {0x1D6FC, 'a', 25}, // 粗斜体
		{0x1D71C, 'A', 25}, {0x1D736, 'a', 25},
		{0x1D756, 'A', 25}, {0x1D770, 'a', 25},
		{0x1D790, 'A', 25}, {0x1D7AA, 'a', 25},
	}
	for _, blk := range blocks {
		if r >= blk.start && r < blk.start+blk.n {
			return blk.base + (r - blk.start), true
		}
	}
	// 数学数字与粗体数字
	dig := []struct {
		start rune
	}{
		{0x1D7CE}, {0x1D7D8}, {0x1D7E2}, {0x1D7EC}, {0x1D7F6},
	}
	for _, d := range dig {
		if r >= d.start && r < d.start+10 {
			return '0' + (r - d.start), true
		}
	}
	return 0, false
}

// mathWords LaTeX 命令被吃掉反斜杠后的常见残留。
//
// 注意：这里只放“不可能出现在普通英文单词里”的长命令。像 int / sum / div / pm
// 这类短命令必须走 wordAware 替换，否则 "key_points" 会被打成 "key_po∫s"——
// 这是 1.2.5 报告里真实出现过的乱码。
var mathWords = strings.NewReplacer(
	"rightarrow", "→",
	"rightarow", "→",
	"rightaarrow", "→",
	"ritarrow", "→",
	"rigtharrow", "→",
	"longrightarrow", "→",
	"Rightarrow", "⇒",
	"Leftrightarrow", "⇔",
	"leftrightarrow", "↔",
	"leftarrow", "←",
	"Rightleftharpoons", "⇌",
	"rightleftharpoons", "⇌",
	"displaystyle", "",
	"limits", "",
)

// shortMathWords 短命令：只在独立出现（前后都不是英文字母）时替换。
var shortMathWords = [][2]string{
	{"times", "×"}, {"div", "÷"}, {"pm", "±"}, {"mp", "∓"},
	{"leq", "≤"}, {"geq", "≥"}, {"neq", "≠"}, {"approx", "≈"},
	{"equiv", "≡"}, {"infty", "∞"}, {"partial", "∂"}, {"nabla", "∇"},
	{"sum", "∑"}, {"prod", "∏"}, {"int", "∫"}, {"cdot", "·"},
	{"ldots", "…"}, {"cdots", "⋯"}, {"quad", " "}, {"qquad", " "},
}

// 希腊字母：模型常直接输出 \Delta、\pi 这类命令。
var greekLetters = strings.NewReplacer(
	"\\Delta", "Δ", "\\delta", "δ", "\\pi", "π", "\\theta", "θ",
	"\\alpha", "α", "\\beta", "β", "\\gamma", "γ", "\\lambda", "λ",
	"\\mu", "μ", "\\nu", "ν", "\\rho", "ρ", "\\sigma", "σ",
	"\\omega", "ω", "\\Omega", "Ω", "\\varphi", "φ", "\\phi", "φ",
	"\\eta", "η", "\\tau", "τ", "\\varepsilon", "ε", "\\epsilon", "ε",
)

var (
	reFrac = regexp.MustCompile(`\\(?:d|t)?frac\s*\{([^{}]*)\}\s*\{([^{}]*)\}`)
	reSqrt = regexp.MustCompile(`\\sqrt\s*\{([^{}]*)\}`)
	rePow  = regexp.MustCompile(`\^\s*\{?\\?circ\}?`)
	reText = regexp.MustCompile(`\\(?:text|mathrm|mathbf|mathit|operatorname)\s*\{([^{}]*)\}`)
	reSup  = regexp.MustCompile(`\^\s*\{([^{}]*)\}`)
	reSub  = regexp.MustCompile(`_\s*\{([^{}]*)\}`)
)

// latexToPlain 把结构化 LaTeX 转成人能读的写法。
// 之前只做“删反斜杠”，于是 \frac{1}{2} 变成 frac{1}{2}、\sqrt{2} 变成 sqrt2，
// 两个式子相邻就粘成 fracsqrt22 —— 报告里最刺眼的那类乱码。
func latexToPlain(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	// 由内向外反复展开，处理 \frac{\sqrt{a}}{b} 这类嵌套。
	for i := 0; i < 6; i++ {
		prev := s
		s = reFrac.ReplaceAllString(s, "($1)/($2)")
		s = reSqrt.ReplaceAllString(s, "√($1)")
		if s == prev {
			break
		}
	}
	s = rePow.ReplaceAllString(s, "°")
	// 积分号必须在这里处理：\\int_0 后面紧跟下划线，wordAware 会把它当成单词的一部分。
	for _, kv := range [][2]string{{"\\iiint", "∭"}, {"\\iint", "∬"}, {"\\int", "∫"}} {
		s = strings.ReplaceAll(s, kv[0], kv[1])
	}
	s = reText.ReplaceAllString(s, "$1")
	s = reSup.ReplaceAllString(s, "^$1")
	s = reSub.ReplaceAllString(s, "_$1")
	s = greekLetters.Replace(s)
	// 定界符与排版命令：删掉但不留残渣。
	for _, cmd := range []string{"\\left", "\\right", "\\big", "\\Big", "\\!", "\\,", "\\;", "\\:", "\\ ", "\\\\"} {
		s = strings.ReplaceAll(s, cmd, "")
	}
	return s
}

// replaceWordAware 只在“独立成词”时替换，避免 int 命中 points/print 之类的子串。
func replaceWordAware(s, word, repl string) string {
	if word == "" || !strings.Contains(s, word) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], word) {
			beforeOK := i == 0 || !isASCIIWordByte(s[i-1])
			next := i + len(word)
			afterOK := next >= len(s) || !isASCIIWordByte(s[next])
			if beforeOK && afterOK {
				b.WriteString(repl)
				i = next
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isASCIIWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func fixMathWords(s string) string {
	s = latexToPlain(s)
	// 先去掉带反斜杠的 LaTeX 定界符与命令，注意不能只删 "left"/"right" 字样，
	// 否则会把 right→ 这类还原结果再吃掉。
	for _, cmd := range []string{"\\begin{aligned}", "\\end{aligned}", "\\begin{array}", "\\end{array}"} {
		s = strings.ReplaceAll(s, cmd, "")
	}
	s = mathWords.Replace(s)
	for _, kv := range shortMathWords {
		s = replaceWordAware(s, kv[0], kv[1])
	}
	// 字符串里的字面 \n、\t 是模型把换行写成转义序列的残留，还原成真换行。
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\t", "  ")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			continue
		}
		b.WriteByte(s[i])
	}
	return mergeArrows(dedupeFormulaEcho(b.String()))
}

// dedupeFormulaEcho 去掉公式被渲染两遍（一遍紧凑、一遍散开）留下的回声。
// 形如 "Ka2(H2CO3)Ka2 (H2 CO3 )"：紧凑形式后面紧跟着同字符的散开形式。
func dedupeFormulaEcho(s string) string {
	var out strings.Builder
	i := 0
loop:
	for i < len(s) {
		j := i
		for j < len(s) && isFormulaByte(s[j]) {
			j++
		}
		if j-i >= 6 {
			compact := s[i:j]
			// A + 前缀(A)：后半段是前半段的开头，说明同一段公式出现了两次
			for split := len(compact) / 2; split < len(compact); split++ {
				a, b := compact[:split], compact[split:]
				if len(a) < 3 || len(b) < 2 || !strings.HasPrefix(a, b) {
					continue
				}
				rest := a[len(b):]
				if rest == "" {
					// "CO2CO2" 这种整体重复
					out.WriteString(a)
					i = j
					continue loop
				}
				// 只有散开形式的后半段真的出现在后面时才认定是回声，避免误删
				if ni := skipEcho(s, j, rest); ni != j {
					out.WriteString(a)
					i = ni
					continue loop
				}
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// skipEcho 从 pos 起跳过「允许空白穿插的 want 字符序列」，不匹配就原样返回 pos。
func skipEcho(s string, pos int, want string) int {
	k := pos
	wi := 0
	for k < len(s) && wi < len(want) {
		c := s[k]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			k++
			continue
		}
		if c != want[wi] {
			return pos
		}
		k++
		wi++
	}
	if wi == len(want) {
		return k
	}
	return pos
}

// mergeArrows 把被拆开的连续箭头合成一个。
func mergeArrows(s string) string {
	for _, p := range [][2]string{{"→\n→", "→"}, {"→  →", "→"}, {"→ →", "→"}} {
		for strings.Contains(s, p[0]) {
			s = strings.ReplaceAll(s, p[0], p[1])
		}
	}
	return s
}

func isFormulaByte(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		return true
	case strings.ContainsRune("()+-\\-*/=^_{}.%,", rune(c)):
		return true
	}
	return false
}

// collapseSplitMath 把被拆成一行一个字符的公式重新拼回去。
func collapseSplitMath(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	buf := ""
	flush := func() {
		if buf != "" {
			out = append(out, buf)
			buf = ""
		}
	}
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		single := t != "" && len([]rune(t)) <= 2 && isMathish(t)
		if single {
			if buf == "" {
				buf = t
			} else {
				buf += t
			}
			continue
		}
		flush()
		out = append(out, ln)
	}
	flush()
	return strings.Join(out, "\n")
}

// isMathish 判断短串是否像公式片段（而不是正常中文/单词）。
func isMathish(t string) bool {
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9':
			continue
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			// 单个拉丁字母更可能是公式（正文里很少单独成行）
			continue
		case strings.ContainsRune("+-*/=<>^_{}().,%|∞≈≠≤≥±×÷→←⇒⇔⇌∑∏∫∂∇·⋯…", r):
			continue
		default:
			return false
		}
	}
	return true
}

// looksLikeRawJSON 判断一段文本是不是没解析成功的原始 JSON。
// 1.2.5 的漏洞：模型常在 JSON 前后加 "json" 字样或代码围栏，甚至被截断成
// 半个对象，旧实现只认 “以 { 开头”，于是整段 JSON 被当成课堂笔记写进了报告。
func looksLikeRawJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	for _, fence := range []string{"```json", "```JSON", "```Json", "```"} {
		t = strings.TrimPrefix(t, fence)
	}
	t = strings.TrimSpace(t)
	keys := 0
	for _, k := range []string{`"board_text"`, `"summary"`, `"notes"`, `"key_points"`,
		`"copying_score"`, `"copy_rate"`, `"copying_evidence"`, `"topic"`, `"clarifications"`} {
		if strings.Contains(s, k) {
			keys++
		}
	}
	if keys >= 2 {
		return true
	}
	if strings.HasPrefix(t, "{") && strings.Count(t, `":`) >= 3 {
		return true
	}
	if strings.HasPrefix(t, "[") && strings.Contains(t, `":`) {
		return true
	}
	return false
}

// looksLikeJSONFragment 判断一段文本是否残留 JSON 结构片段（截断的中间段）。
// 这类文本没法还原成笔记，硬留着只会在报告里显示成一堆引号和花括号。
func looksLikeJSONFragment(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	markers := 0
	for _, m := range []string{`"content":`, `"step":`, `"basis":`, `"step_id"`, `"}`, `"],`, `"subject":`} {
		if strings.Contains(t, m) {
			markers++
		}
	}
	return markers >= 2
}
