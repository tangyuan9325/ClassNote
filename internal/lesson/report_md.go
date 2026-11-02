package lesson

import (
	"html/template"
	"strconv"
	"strings"
)

// renderNotes 把模型给出的 Markdown 笔记渲染成受控 HTML。
//
// 1.2.5 用 nl2br 直接输出，于是报告里出现大量字面的
// "## 核心知识点梳理" 和 "- 压强单位换算"——模型写的是 Markdown，
// 报告却当成纯文本打印，看起来就像乱码。
//
// 安全约定：先整体 HTML 转义，再按行生成标签，模型输出永远进不了标签内部。
func renderNotes(s string) template.HTML {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	inList := false
	closeList := func() {
		if inList {
			b.WriteString("</ul>")
			inList = false
		}
	}
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			closeList()
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "### "):
			closeList()
			b.WriteString("<h4>" + inlineMD(strings.TrimPrefix(trimmed, "### ")) + "</h4>")
		case strings.HasPrefix(trimmed, "## "):
			closeList()
			b.WriteString("<h3>" + inlineMD(strings.TrimPrefix(trimmed, "## ")) + "</h3>")
		case strings.HasPrefix(trimmed, "# "):
			closeList()
			b.WriteString("<h3>" + inlineMD(strings.TrimPrefix(trimmed, "# ")) + "</h3>")
		case isBullet(trimmed):
			if !inList {
				b.WriteString("<ul>")
				inList = true
			}
			b.WriteString("<li>" + inlineMD(stripBullet(trimmed)) + "</li>")
		default:
			closeList()
			b.WriteString("<p>" + inlineMD(trimmed) + "</p>")
		}
	}
	closeList()
	return template.HTML(b.String())
}

func isBullet(s string) bool {
	for _, p := range []string{"- ", "* ", "• ", "· "} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func stripBullet(s string) string {
	for _, p := range []string{"- ", "* ", "• ", "· "} {
		if strings.HasPrefix(s, p) {
			return strings.TrimSpace(strings.TrimPrefix(s, p))
		}
	}
	return s
}

// inlineMD 处理行内标记：**粗体**、`代码`、~~删除~~。
// 输入先转义，因此这里只可能遇到自己生成的实体与普通字符。
func inlineMD(s string) string {
	esc := template.HTMLEscapeString(s)
	esc = boldTag(esc, "**")
	esc = boldTag(esc, "__")
	return codeTag(esc)
}

// boldTag 成对替换定界符，落单的保留原文。
func boldTag(s, mark string) string {
	parts := strings.Split(s, mark)
	if len(parts) < 3 {
		return s
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("<strong>" + p + "</strong>")
		} else {
			b.WriteString(p)
		}
	}
	return b.String()
}

func codeTag(s string) string {
	parts := strings.Split(s, "`")
	if len(parts) < 3 {
		return s
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + p + "</code>")
		} else {
			b.WriteString(p)
		}
	}
	return b.String()
}

// scoreLabel 把 -1（未判定）翻译成人能读的文案。
// 1.2.5 的报告直接打印 "照搬评分 -1"，看起来像出错。
func scoreLabel(score int) string {
	if score < 0 {
		return "未判定"
	}
	return strconv.Itoa(score)
}
