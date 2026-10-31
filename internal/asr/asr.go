// Package asr 提供语音转写能力：OpenAI 兼容接口或本地 whisper.cpp。
package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"classnote/internal/audio"
	"classnote/internal/config"
)

// Transcriber 执行语音转写。
type Transcriber struct {
	cfg config.ASRConfig
}

// New 创建转写器。
func New(cfg config.ASRConfig) *Transcriber { return &Transcriber{cfg: cfg} }

// Update 更新配置。
func (t *Transcriber) Update(cfg config.ASRConfig) { t.cfg = cfg }

// Ready 判断是否可用。
func (t *Transcriber) Ready() bool {
	switch t.cfg.Mode {
	case "api":
		return t.cfg.BaseURL != "" && t.cfg.Model != ""
	case "local":
		// 有本地模型文件即可；服务常驻时连文件路径都可以自动探测。
		if t.cfg.LocalModel != "" {
			return true
		}
		return LocalHealth(t.cfg.LocalPort)
	}
	return false
}

// Describe 返回后端描述。
func (t *Transcriber) Describe() string {
	switch t.cfg.Mode {
	case "api":
		return "接口转写 · " + t.cfg.Model
	case "local":
		if t.cfg.LocalPort > 0 && LocalHealth(t.cfg.LocalPort) {
			return fmt.Sprintf("本地 whisper.cpp · 服务运行中（%d）", t.cfg.LocalPort)
		}
		return "本地 whisper.cpp"
	}
	return "未启用"
}

// Transcribe 把 PCM16 单声道音频转成文字。
func (t *Transcriber) Transcribe(ctx context.Context, pcm []byte, sampleRate int) (string, error) {
	if len(pcm) < 3200 {
		return "", nil
	}
	wav := audio.EncodeWAV(pcm, sampleRate)
	switch t.cfg.Mode {
	case "api":
		return t.transcribeAPI(ctx, wav)
	case "local":
		return t.transcribeLocal(ctx, wav)
	default:
		return "", fmt.Errorf("语音转写未启用，请在设置中选择转写方式")
	}
}

func (t *Transcriber) transcribeAPI(ctx context.Context, wav []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	_ = mw.WriteField("model", t.cfg.Model)
	if t.cfg.Language != "" {
		_ = mw.WriteField("language", t.cfg.Language)
	}
	_ = mw.WriteField("response_format", "json")
	_ = mw.WriteField("temperature", "0")
	// 领域提示词，能明显提升课堂术语的准确率。
	_ = mw.WriteField("prompt", "课堂讲课内容，涉及语文课文、古诗文、修辞手法、写作方法。")
	if err := mw.Close(); err != nil {
		return "", err
	}
	url := strings.TrimRight(t.cfg.BaseURL, "/")
	if !strings.HasSuffix(url, "/audio/transcriptions") {
		url += "/audio/transcriptions"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if t.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.cfg.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("转写请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("转写接口返回 %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Text   string `json:"text"`
		Output struct {
			Text string `json:"text"`
		} `json:"output"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析转写结果失败: %s", truncate(string(raw), 160))
	}
	text := firstNonEmpty(out.Text, out.Output.Text, out.Result)
	return strings.TrimSpace(text), nil
}

func (t *Transcriber) transcribeLocal(ctx context.Context, wav []byte) (string, error) {
	// 优先走常驻的 whisper-server：模型只加载一次，每段音频几百毫秒出结果。
	if t.cfg.LocalPort > 0 && LocalHealth(t.cfg.LocalPort) {
		return LocalTranscribe(ctx, t.cfg.LocalPort, wav, t.cfg.Language)
	}
	if localSrv != nil && localSrv.Alive() {
		return LocalTranscribe(ctx, localSrv.Port(), wav, t.cfg.Language)
	}
	// 回退到 whisper-cli（每段重新加载模型，慢，但不依赖服务）。
	dir, err := os.MkdirTemp("", "cn-asr-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "chunk.wav")
	if err := os.WriteFile(in, wav, 0o644); err != nil {
		return "", err
	}
	args := []string{"-m", t.cfg.LocalModel, "-f", in, "-nt", "-otxt", "-of", filepath.Join(dir, "out")}
	lang := t.cfg.Language
	if lang == "" {
		lang = "zh"
	}
	args = append(args, "-l", lang)
	cmd := exec.CommandContext(ctx, t.cfg.LocalExe, args...)
	cmd.Dir = filepath.Dir(t.cfg.LocalExe)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("本地转写失败: %v %s", err, truncate(string(out), 200))
	}
	// whisper.cpp 的 -otxt 会生成 out.txt。
	if b, err := os.ReadFile(filepath.Join(dir, "out.txt")); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(string(out)), nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
