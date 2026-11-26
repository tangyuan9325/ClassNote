package lesson

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"classnote/internal/capture"
	"classnote/internal/llm"
	"classnote/internal/logx"
)

// ShotResult 是「识图」的结果：截图 → 文字识别 → AI 讲解。
type ShotResult struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Image   string    `json:"image"` // 相对 /api/media/ 的路径
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	OCRText string    `json:"ocrText"`
	OCRMs   int       `json:"ocrMs"`
	Title   string    `json:"title"`
	Explain string    `json:"explain"`
	Points  []string  `json:"points"`
	Ask     []string  `json:"ask"`
	Model   string    `json:"model"`
	Tokens  int       `json:"tokens"`
	Elapsed int       `json:"elapsedMs"`
	// Warning 是「能出结果但不完整」的说明，例如 OCR 服务没启动、
	// 只能靠视觉模型看图，或者模型没按 JSON 回。
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
}

// QuickShot 抓当前画面并立刻交给 AI 讲一遍。
//
// 与课堂里的逐页分析分开：这里不写分段、不进报告，随时可用（没在上课也行），
// 结果只推给界面上的识图面板。
func (e *Engine) QuickShot(ctx context.Context) (*ShotResult, error) {
	start := time.Now()

	e.mu.Lock()
	client := e.client
	subject := ""
	if e.sess != nil {
		subject = e.sess.Subject
	}
	capDir := e.captures
	vision := e.cfg.Provider.Vision
	quality := e.cfg.Capture.JPEGQuality
	e.mu.Unlock()
	if quality <= 0 {
		quality = 88
	}
	if capDir == "" {
		// 没在上课：截图放在 captures/shots 下，媒体接口照样能读。
		capDir = filepath.Join(e.paths.Captures, "shots")
	}
	if err := os.MkdirAll(capDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建截图目录失败：%w", err)
	}

	snap, err := e.det.CaptureNow()
	if err != nil {
		return nil, fmt.Errorf("截图失败：%w", err)
	}
	if snap == nil || snap.Image == nil {
		return nil, fmt.Errorf("没有抓到画面")
	}

	id := fmt.Sprintf("shot-%s", time.Now().Format("20060102-150405"))
	name := id + ".jpg"
	if err := capture.SaveImage(snap.Image, filepath.Join(capDir, name), quality); err != nil {
		return nil, fmt.Errorf("保存截图失败：%w", err)
	}
	imgRaw, _ := capture.EncodeJPEG(snap.Image, quality)

	out := &ShotResult{
		ID: id, At: time.Now(), Width: snap.Width, Height: snap.Height,
		Image: filepath.ToSlash(filepath.Join(filepath.Base(capDir), name)),
	}

	// 1) 本地 OCR：拿到文字，视觉模型没开时它就是唯一依据。
	if len(imgRaw) > 0 && e.wantOCR("") {
		if res, err := e.recognizeImage(ctx, imgRaw, fmt.Sprintf("%016x", snap.Hash)); err == nil {
			out.OCRText, out.OCRMs = res.Text, res.LatencyMS
			if res.Truncated {
				out.Warning = "识别文字过长，已截断"
			}
		} else if !errors.Is(err, errOCROff) {
			out.Warning = "画面识别不可用：" + err.Error()
			logx.Warnf("lesson", "识图时画面识别失败: %v", err)
		}
	} else if e.ocrCooling() {
		out.Warning = "画面识别服务冷却中，本次只用视觉模型"
	}

	// 2) AI 讲解。
	if client == nil {
		if strings.TrimSpace(out.OCRText) == "" {
			return nil, fmt.Errorf("模型还没配置好，识别也没拿到文字；请先在设置里选择模型")
		}
		out.Title = "画面文字"
		out.Explain = "（模型未配置，这里只有识别出的文字）\n\n" + out.OCRText
		out.Elapsed = int(time.Since(start).Milliseconds())
		return out, nil
	}

	msgs := []llm.Message{llm.TextMessage(llm.RoleSystem, shotSystem)}
	prompt := shotUser(subject, out.OCRText)
	if len(imgRaw) > 0 && vision {
		msgs = append(msgs, llm.UserVision(prompt, base64.StdEncoding.EncodeToString(imgRaw), "image/jpeg"))
	} else {
		if strings.TrimSpace(out.OCRText) == "" {
			return nil, fmt.Errorf("视觉模型已关闭，识别也没拿到文字，无法分析这张截图")
		}
		msgs = append(msgs, llm.UserText(prompt+"\n\n（当前没有把图片发给模型，只能依据 OCR 文本作答；不确定的地方请直接说明）"))
		out.Warning = joinWarning(out.Warning, "视觉模型已关闭，本次只依据识别文字")
	}

	chatCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	// FollowUp=false：识图是独立的一次提问，不该把网页版那条学科对话当成上下文。
	res, cerr := client.Chat(chatCtx, msgs, llm.Options{
		Subject: subject, Temperature: 0.2, MaxTokens: 1200, JSON: true,
	})
	if cerr != nil {
		if strings.TrimSpace(out.OCRText) != "" {
			out.Title = "画面文字"
			out.Explain = out.OCRText
			out.Warning = joinWarning(out.Warning, "AI 讲解失败："+cerr.Error())
			out.Elapsed = int(time.Since(start).Milliseconds())
			return out, nil
		}
		return nil, fmt.Errorf("AI 讲解失败：%w", cerr)
	}

	out.Model = res.Model
	out.Tokens = res.Usage.TotalTokens
	applyShotOut(out, res.Text)
	if strings.TrimSpace(out.Explain) == "" && strings.TrimSpace(out.OCRText) != "" {
		out.Explain = out.OCRText
	}
	if strings.TrimSpace(out.Title) == "" {
		out.Title = "画面内容"
	}
	out.Elapsed = int(time.Since(start).Milliseconds())
	logx.Infof("lesson", "识图完成：%s（%d 字，%d tokens，%d ms）",
		out.Title, len([]rune(out.Explain)), out.Tokens, out.Elapsed)
	return out, nil
}

// ocrCooling 表示服务刚失败过、正在冷却。
func (e *Engine) ocrCooling() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.ocrFailUntil.IsZero() && time.Now().Before(e.ocrFailUntil)
}

func joinWarning(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || strings.Contains(a, b) {
		return a
	}
	return a + "；" + b
}

// applyShotOut 解析模型的 JSON，失败时把原文当讲解——识图是即时工具，
// 宁可显示一段没结构的文字，也不要弹「解析失败」让用户重截一次。
func applyShotOut(out *ShotResult, text string) {
	raw, ok := llm.ExtractJSON(text)
	if !ok {
		out.Explain = strings.TrimSpace(text)
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		out.Explain = strings.TrimSpace(text)
		return
	}
	if v := shotField(m, "title", "heading", "name", "标题"); v != "" {
		out.Title = v
	}
	if v := shotField(m, "explain", "explanation", "notes", "note", "content", "answer", "讲解"); v != "" {
		out.Explain = v
	}
	if v := shotField(m, "ocr_text", "ocrText", "board_text", "text"); v != "" && strings.TrimSpace(out.OCRText) == "" {
		out.OCRText = v
	}
	if v := shotList(m, "points", "key_points", "keyPoints", "要点"); len(v) > 0 {
		out.Points = v
	}
	if v := shotList(m, "ask", "questions", "followups", "追问"); len(v) > 0 {
		out.Ask = v
	}
}

func shotField(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := rawToString(v); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func shotList(m map[string]json.RawMessage, keys ...string) []string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		var list []string
		if err := json.Unmarshal(v, &list); err == nil {
			out := make([]string, 0, len(list))
			for _, s := range list {
				if t := strings.TrimSpace(s); t != "" {
					out = append(out, t)
				}
			}
			return out
		}
		if s, ok := rawToString(v); ok {
			var out []string
			for _, part := range strings.FieldsFunc(s, func(r rune) bool {
				return r == '\n' || r == '；' || r == ';'
			}) {
				if t := strings.TrimSpace(strings.TrimLeft(part, "-·• ")); t != "" {
					out = append(out, t)
				}
			}
			return out
		}
	}
	return nil
}
