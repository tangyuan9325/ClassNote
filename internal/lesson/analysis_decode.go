package lesson

import (
	"encoding/json"
	"errors"
	"strings"

	"classnote/internal/llm"
)

// 本文件处理“模型输出结构与约定不一致”的问题。
//
// 1.2.5 实机日志里出现过三类解析失败，全部来自网页版模型：
//   json: cannot unmarshal array into Go struct field .notes of type string
//   json: cannot unmarshal number 0.85 into Go struct field .copying_score of type int
//   invalid character '1' looking for beginning of object key string
//
// 三者都会让整页分析作废，最后落到“把原始 JSON 当笔记”的兜底路径，
// 报告里就出现大段花括号和引号。这里在解析前把字段规整成约定类型。

// analysisOut 是单页分析的结构化结果。
type analysisOut struct {
	BoardText       string   `json:"board_text"`
	Summary         string   `json:"summary"`
	Notes           string   `json:"notes"`
	KeyPoints       []string `json:"key_points"`
	Tips            []string `json:"tips"`
	IsCopying       *bool    `json:"is_copying"`
	CopyingScore    int      `json:"copying_score"`
	CopyingEvidence string   `json:"copying_evidence"`
}

// reportOut 是课堂报告的结构化结果。
type reportOut struct {
	Title       string   `json:"title"`
	CopyRate    int      `json:"copy_rate"`
	Summary     string   `json:"summary"`
	Highlights  []string `json:"highlights"`
	Problems    []string `json:"problems"`
	Suggestions []string `json:"suggestions"`
}

// coerceJSONFields 把模型写错的字段类型改写成约定类型，返回新的 JSON 文本。
// 解析失败时原样返回，交给调用方的兜底逻辑。
func coerceJSONFields(raw string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	changed := false

	// 0) 拆掉 {"data": {...}} / {"result": {...}} 这层外壳。
	if inner, ok := unwrapEnvelope(m); ok {
		var m2 map[string]json.RawMessage
		if err := json.Unmarshal(inner, &m2); err == nil {
			m = m2
			changed = true
		}
	}

	// 1) 字段别名归一。
	// 网页版模型在拿不到输出格式时，会把整段笔记塞进 {"content": "..."}——
	// JSON 合法，但 board_text/notes/summary 全缺，解析不报错、字段全空。
	for _, g := range aliasGroups {
		if _, ok := m[g.canonical]; ok {
			continue
		}
		for _, a := range g.aliases {
			if v, ok := m[a]; ok {
				m[g.canonical] = v
				changed = true
				break
			}
		}
	}

	// 文本字段：数组 → 用换行拼接；数字 → 十进制字符串。
	for _, key := range []string{"board_text", "summary", "notes", "copying_evidence"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		if s, ok := rawToString(v); ok {
			if b, err := json.Marshal(s); err == nil && string(b) != string(v) {
				m[key] = b
				changed = true
			}
		}
	}

	// 列表字段：字符串 → 按行/分号切开。
	for _, key := range []string{"key_points", "tips", "highlights", "problems", "suggestions"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			var list []string
			for _, part := range strings.FieldsFunc(s, func(r rune) bool {
				return r == '\n' || r == '；' || r == ';'
			}) {
				if p := strings.TrimSpace(strings.TrimLeft(part, "-·• ")); p != "" {
					list = append(list, p)
				}
			}
			if list == nil {
				list = []string{}
			}
			if b, err := json.Marshal(list); err == nil {
				m[key] = b
				changed = true
			}
		}
	}

	// 分数：0-1 的浮点归一化到百分制（模型常把 85 写成 0.85）。
	for _, key := range []string{"copying_score", "copy_rate"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		var f float64
		if err := json.Unmarshal(v, &f); err != nil {
			continue
		}
		n := int(f)
		if f > 0 && f <= 1 {
			n = int(f*100 + 0.5)
		}
		if b, err := json.Marshal(n); err == nil {
			m[key] = b
			changed = true
		}
	}

	if !changed {
		return raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return string(out)
}

// aliasGroup 描述一个字段的规范名与模型可能用的别名。
type aliasGroup struct {
	canonical string
	aliases   []string
}

var aliasGroups = []aliasGroup{
	{"board_text", []string{"board", "slide_text", "screen_text", "text_on_slide", "boardText"}},
	{"summary", []string{"abstract", "overview", "brief", "one_line", "summary_text", "一句话概括"}},
	{"notes", []string{"content", "text", "answer", "body", "markdown", "note", "detailed_notes", "笔记"}},
	{"copying_score", []string{"copy_score", "copying_rate", "score", "copyRate"}},
	{"copying_evidence", []string{"evidence", "copy_evidence", "reason", "copying_reason"}},
	{"is_copying", []string{"is_copying_ppt", "copying", "isCopying"}},
	{"key_points", []string{"keyPoints", "points", "要点"}},
	{"tips", []string{"tip", "warnings", "提示"}},
}

// unwrapEnvelope 处理 {"data": {...}} 这类多包一层的返回。
func unwrapEnvelope(m map[string]json.RawMessage) (json.RawMessage, bool) {
	if len(m) != 1 {
		return nil, false
	}
	for _, key := range []string{"data", "result", "response", "output", "payload"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		t := strings.TrimSpace(string(v))
		if strings.HasPrefix(t, "{") {
			return v, true
		}
	}
	return nil, false
}

// rawToString 把 JSON 的字符串/数组/数字统一读成字符串。
func rawToString(v json.RawMessage) (string, bool) {
	t := strings.TrimSpace(string(v))
	if t == "" || t == "null" {
		return "", false
	}
	switch t[0] {
	case '"':
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s, true
		}
	case '[':
		var list []json.RawMessage
		if err := json.Unmarshal(v, &list); err == nil {
			parts := make([]string, 0, len(list))
			for _, item := range list {
				if s, ok := rawToString(item); ok && strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			return strings.Join(parts, "\n"), true
		}
	default:
		if t[0] == '-' || t[0] >= '0' && t[0] <= '9' {
			return t, true
		}
	}
	return "", false
}

// decodeAnalysis 解析单页分析结果，尽量不丢内容。
func decodeAnalysis(text string) (analysisOut, error) {
	var out analysisOut
	raw, ok := llm.ExtractJSON(text)
	if !ok {
		// 括号没配平（回复被页面截断）。整段没法解析，但已经写完整的字段
		// 还能按名字捞出来——救助多少算多少，比整页丢掉强。
		if salvageAnalysisFields(text, &out) {
			return out, nil
		}
		return out, errNoJSON
	}
	if err := json.Unmarshal([]byte(coerceJSONFields(raw)), &out); err == nil {
		// 解析成功不等于拿到了内容。1.2.5 的实机故障就是：模型返回
		// {"content": "..."}（合法 JSON、字段名不匹配），Unmarshal 不报错，
		// 结构体全是零值，报告里从第 2 页起只剩一句「照搬评分 -1」，
		// 而且日志只写「分析完成」，看不出异常。这里把"空壳"当失败处理。
		if hasAnalysisContent(out) {
			return out, nil
		}
	}
	// 仍然失败（典型：非法转义、裸控制字符）。逐字段抢救，能救多少算多少。
	if !salvageAnalysisFields(raw, &out) {
		return out, errNoJSON
	}
	return out, nil
}

// salvageAnalysisFields 按字段名从残缺 JSON 里捞值，返回是否捞到了内容。
func salvageAnalysisFields(raw string, out *analysisOut) bool {
	out.Notes = extractFieldAny(raw, "notes")
	out.Summary = extractFieldAny(raw, "summary")
	out.BoardText = extractFieldAny(raw, "board_text")
	out.CopyingEvidence = extractFieldAny(raw, "copying_evidence")
	return hasAnalysisContent(*out)
}

// hasAnalysisContent 判断解析结果里是否真的有内容。
func hasAnalysisContent(o analysisOut) bool {
	return strings.TrimSpace(o.Notes) != "" ||
		strings.TrimSpace(o.Summary) != "" ||
		strings.TrimSpace(o.BoardText) != "" ||
		strings.TrimSpace(o.CopyingEvidence) != "" ||
		len(o.KeyPoints) > 0 || len(o.Tips) > 0
}

// decodeReport 解析报告结果，同样做类型规整。
func decodeReport(text string) (reportOut, error) {
	var out reportOut
	raw, ok := llm.ExtractJSON(text)
	if !ok {
		return out, errNoJSON
	}
	if err := json.Unmarshal([]byte(coerceJSONFields(raw)), &out); err == nil {
		return out, nil
	}
	out.Title = extractField(raw, "title")
	out.Summary = extractField(raw, "summary")
	if out.Summary == "" && out.Title == "" {
		return out, errNoJSON
	}
	return out, nil
}

// errNoJSON 表示模型回复里找不到可解析的 JSON。
var errNoJSON = errors.New("回复里没有可解析的 JSON")

// extractFieldAny 按规范名与其别名依次尝试取值。
func extractFieldAny(raw, canonical string) string {
	if v := extractField(raw, canonical); v != "" {
		return v
	}
	for _, g := range aliasGroups {
		if g.canonical != canonical {
			continue
		}
		for _, a := range g.aliases {
			if v := extractField(raw, a); v != "" {
				return v
			}
		}
	}
	return ""
}
