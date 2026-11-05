package lesson

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"classnote/internal/asrtext"
	"classnote/internal/llm"
	"classnote/internal/logx"
	"classnote/internal/store"
)

type reportView struct {
	Title       string
	Subject     string
	Date        string
	TimeRange   string
	Duration    string
	Slides      int
	Words       int
	CopyRate    int
	CopyLabel   string
	Summary     string
	Highlights  []string
	Problems    []string
	Suggestions []string
	Model       string
	Pages       []reportPage
	Transcript  []reportLine
}

type reportPage struct {
	Index      int
	SlideIndex int
	Summary    string
	Notes      string
	KeyPoints  []string
	Tips       []string
	Copying    bool
	CopyScore  int
	ScoreLabel string
	Evidence   string
	BoardText  string
	Image      template.URL
	Transcript string
	TimeLabel  string
}

type reportLine struct {
	Index int
	Time  string
	Text  string
}

// GenerateReport 生成 HTML 与 PDF 报告。
func (e *Engine) GenerateReport(sess *store.Session) error {
	if sess == nil {
		return fmt.Errorf("没有可生成报告的课堂")
	}
	e.mu.Lock()
	client := e.client
	captures := e.captures
	e.mu.Unlock()
	if captures == "" {
		captures = filepath.Join(e.paths.Captures, sess.ID)
	}

	view := reportView{
		Title:   sess.Title,
		Subject: sess.Subject,
		Date:    sess.StartedAt.Format("2006年01月02日"),
		Model:   sess.AIModel,
		Slides:  len(sess.Segments),
	}
	end := time.Now()
	if sess.EndedAt != nil {
		end = *sess.EndedAt
	}
	view.TimeRange = sess.StartedAt.Format("15:04") + " - " + end.Format("15:04")
	for _, seg := range sess.Segments {
		// 只统计清洗后的有效转写，别再拿幻觉文本凑字数。
		view.Words += len([]rune(asrtext.CleanForReport(seg.Transcript)))
	}
	dur := end.Sub(sess.StartedAt)
	view.Duration = fmt.Sprintf("%d 分钟", int(dur.Minutes()))

	totalScore, weight := 0, 0
	for _, seg := range sess.Segments {
		// 渲染前统一清洗：老会话里可能存着没解析成功的 JSON 或数学乱码。
		// 注意顺序——必须先在**原始文本**上判断是不是 JSON，再规范化；
		// 规范化会把引号/下划线改得面目全非，判断就失效了（1.2.5 的实际问题）。
		rawNotes := seg.Notes
		if looksLikeRawJSON(rawNotes) || looksLikeJSONFragment(rawNotes) {
			if n := extractField(rawNotes, "notes"); n != "" {
				rawNotes = n
			} else {
				rawNotes = ""
			}
		}
		summary := normalizeModelText(seg.Summary)
		notes := normalizeModelText(rawNotes)
		evidence := normalizeModelText(seg.CopyEvidence)
		// 先清 ASR 幻觉再规范化：老会话里可能已经存进了大段复读的幻觉文本。
		transcript := normalizeModelText(asrtext.CleanForReport(seg.Transcript))
		board := normalizeModelText(seg.BoardText)
		if looksLikeRawJSON(summary) || looksLikeJSONFragment(summary) || strings.TrimSpace(summary) == "{" {
			summary = firstLine(notes)
		}
		if strings.TrimSpace(board) == "" {
			board = normalizeModelText(seg.BoardText)
		}
		kp := make([]string, 0, len(seg.KeyPoints))
		for _, k := range seg.KeyPoints {
			if v := normalizeModelText(k); v != "" {
				kp = append(kp, v)
			}
		}
		tp := make([]string, 0, len(seg.Tips))
		for _, k := range seg.Tips {
			if v := normalizeModelText(k); v != "" {
				tp = append(tp, v)
			}
		}
		cp := reportPage{
			Index: seg.Index, SlideIndex: seg.SlideIndex, Summary: summary,
			Notes: notes, KeyPoints: kp, Tips: tp,
			Copying: seg.Copying, CopyScore: seg.CopyScore, ScoreLabel: scoreLabel(seg.CopyScore), Evidence: evidence,
			Transcript: transcript,
			BoardText:  board,
			TimeLabel:  seg.CreatedAt.Format("15:04"),
		}
		w := len([]rune(seg.Transcript)) + 40
		if seg.CopyScore >= 0 {
			totalScore += seg.CopyScore * w
			weight += w
		}
		if seg.ImageFile != "" {
			if raw, err := os.ReadFile(filepath.Join(captures, seg.ImageFile)); err == nil {
				// data: 协议会被 html/template 的安全过滤拦成 #ZgotmplZ，
				// 这里显式标记为可信 URL（内容是我们自己读的本地截图）。
				cp.Image = template.URL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw))
			}
		}
		if strings.TrimSpace(cp.Transcript) != "" {
			view.Transcript = append(view.Transcript, reportLine{Index: seg.Index, Time: cp.TimeLabel, Text: cp.Transcript})
		}
		view.Pages = append(view.Pages, cp)
	}
	if weight > 0 {
		view.CopyRate = totalScore / weight
	}
	speech := false
	for _, seg := range sess.Segments {
		if len([]rune(seg.Transcript)) > 30 {
			speech = true
			break
		}
	}
	switch {
	case !speech:
		// 全程没有语音，不能给出“以讲解为主”这种结论。
		view.CopyLabel = "无语音证据"
	default:
		view.CopyLabel = copyLabel(view.CopyRate)
	}
	view.Summary = defaultSummary(view.CopyRate, len(view.Pages), speech)

	// 让模型写一段整体评价。
	if client != nil && speech {
		if sum, ok := e.aiReportSummary(client, sess); ok {
			view.Summary = sum.summary
			view.CopyRate = sum.copyRate
			view.CopyLabel = copyLabel(sum.copyRate)
			view.Highlights = sum.highlights
			view.Problems = sum.problems
			view.Suggestions = sum.suggestions
			if sum.title != "" {
				view.Title = sum.title
			}
		}
	}

	html, err := renderReport(view)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.paths.Reports, 0o755); err != nil {
		return err
	}
	htmlPath := filepath.Join(e.paths.Reports, sess.ID+".html")
	if err := os.WriteFile(htmlPath, html, 0o644); err != nil {
		return err
	}
	pdfPath, pdfErr := htmlToPDF(htmlPath)
	if pdfErr != nil {
		logx.Warnf("report", "PDF 生成失败: %v", pdfErr)
	}

	rep := &store.Report{
		CreatedAt: time.Now(), HTMLPath: htmlPath, PDFPath: pdfPath,
		Copying: view.CopyRate >= CopyFlagFrom, CopyRate: view.CopyRate,
		Summary: view.Summary, Title: view.Title,
	}

	e.mu.Lock()
	if e.sess != nil && e.sess.ID == sess.ID {
		e.sess.Report = rep
	}
	e.mu.Unlock()
	sess.Report = rep
	if err := e.store.Save(sess); err != nil {
		return err
	}
	e.emit("report", rep)
	logx.Infof("report", "报告已生成: %s", htmlPath)
	return nil
}

type aiReport struct {
	summary     string
	copyRate    int
	highlights  []string
	problems    []string
	suggestions []string
	title       string
}

func (e *Engine) aiReportSummary(client llm.Client, sess *store.Session) (aiReport, bool) {
	var sb strings.Builder
	for _, seg := range sess.Segments {
		if seg.Summary == "" && seg.Notes == "" {
			continue
		}
		fmt.Fprintf(&sb, "\n第 %d 页：%s\n笔记：%s\n照搬评分：%d\n证据：%s\n",
			seg.Index, seg.Summary, truncate(seg.Notes, 400), seg.CopyScore, seg.CopyEvidence)
	}
	if sb.Len() < 40 {
		return aiReport{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	res, err := client.Chat(ctx,
		[]llm.Message{
			llm.TextMessage(llm.RoleSystem, reportSystem),
			llm.UserText(reportUser(sess.Subject, sb.String())),
		},
		llm.Options{Temperature: 0.3, MaxTokens: 1500, JSON: true})
	if err != nil {
		logx.Warnf("report", "生成总体评价失败: %v", err)
		return aiReport{}, false
	}
	out, derr := decodeReport(res.Text)
	if derr != nil {
		logx.Warnf("report", "报告 JSON 解析失败: %v", derr)
		return aiReport{}, false
	}
	return aiReport{
		summary: out.Summary, copyRate: clamp(out.CopyRate), highlights: out.Highlights,
		problems: out.Problems, suggestions: out.Suggestions, title: out.Title,
	}, true
}

func defaultSummary(rate, pages int, speech bool) string {
	if !speech {
		return fmt.Sprintf("本堂课共记录 %d 个片段，但全程没有可用的语音转写，无法判断教师是否照搬照抄念 PPT。以下笔记仅依据课件文字与画面整理。", pages)
	}
	return fmt.Sprintf("本堂课共记录 %d 个讲解片段。按语音转写与画面文字的对照，整堂课照搬程度约 %d%%，%s。", pages, rate, copyLabel(rate))
}

// htmlToPDF 把报告 HTML 渲染为 PDF：优先用 CDP 打印，失败时回退到命令行。
func htmlToPDF(htmlPath string) (string, error) {
	pdfPath := strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)) + ".pdf"
	_ = os.Remove(pdfPath)
	profile := filepath.Join(filepath.Dir(filepath.Dir(htmlPath)), "pdfprofile")
	if err := llm.RenderPDF(htmlPath, pdfPath, profile); err == nil {
		return pdfPath, nil
	} else {
		logx.Warnf("report", "CDP 打印失败，尝试命令行: %v", err)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(htmlPath)}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-pdf-header-footer",
		"--print-to-pdf-no-header",
		"--print-to-pdf=" + pdfPath,
		u.String(),
	}
	var lastErr error
	for _, exe := range browserCandidates() {
		cmd := exec.Command(exe, args...)
		cmd.Dir = filepath.Dir(htmlPath)
		out, err := cmd.CombinedOutput()
		if fi, e := os.Stat(pdfPath); e == nil && fi.Size() > 1000 {
			return pdfPath, nil
		}
		if err != nil {
			lastErr = fmt.Errorf("%s: %v %s", filepath.Base(exe), err, truncate(string(out), 160))
			continue
		}
		lastErr = fmt.Errorf("%s 未生成 PDF", filepath.Base(exe))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未找到 Edge 或 Chrome")
	}
	return "", lastErr
}

func browserCandidates() []string {
	var out []string
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if base == "" {
			continue
		}
		out = append(out,
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	var exist []string
	for _, p := range out {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			exist = append(exist, p)
		}
	}
	return exist
}

var reportTpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"nl2br": func(s string) template.HTML {
		esc := template.HTMLEscapeString(s)
		esc = strings.ReplaceAll(esc, "\n", "<br>")
		return template.HTML(esc)
	},
	"pct": func(v int) string { return fmt.Sprintf("%d%%", v) },
	// notes 渲染受控 Markdown：模型写的是 "## 标题 / - 列表"，直接 nl2br 会原样打印。
	"notes": func(s string) template.HTML { return renderNotes(s) },
}).Parse(reportHTML))

func renderReport(v reportView) ([]byte, error) {
	var buf bytes.Buffer
	if err := reportTpl.Execute(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
