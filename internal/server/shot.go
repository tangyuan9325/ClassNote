package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"classnote/internal/capture"
	"classnote/internal/config"
	"classnote/internal/lesson"
	"classnote/internal/logx"
	"classnote/internal/ocr"
)

// ocrConfig 把配置转成识别客户端参数。
func ocrConfig(c config.OCRConfig) ocr.Settings {
	return ocr.Settings{
		Enabled: c.Enabled, BaseURL: c.BaseURL, APIKey: c.APIKey,
		Model: c.Model, Prompt: c.Prompt,
		TimeoutSec: c.TimeoutSec, MaxChars: c.MaxChars,
	}
}

// runQuickShot 执行一次识图并把结果推给界面。
func (s *Server) runQuickShot() (*lesson.ShotResult, error) {
	// 连按快捷键时只认第一次：识别 + 讲解要十几秒，
	// 叠着跑既抢显存，也会让后一次把前一次的面板覆盖掉。
	if !s.shotBusy.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("上一次识图还没完成，稍等一下")
	}
	defer s.shotBusy.Store(false)
	// 先播一条「开始了」：热键按下到出结果要十几秒，界面得先有个反馈。
	s.engine.EmitExternal("shot_started", map[string]any{"at": time.Now()})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	res, err := s.engine.QuickShot(ctx)
	if err != nil {
		s.engine.EmitExternal("shot_error", map[string]any{"message": err.Error()})
		return nil, err
	}
	s.engine.EmitExternal("quick_shot", res)
	return res, nil
}

// handleShot 是界面「识图」按钮的入口。
func (s *Server) handleShot(w http.ResponseWriter, r *http.Request) {
	res, err := s.runQuickShot()
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "shot": res})
}

// TriggerShot 供全局热键调用：立刻返回，结果走 SSE 事件。
// done 用于托盘提示这类不依赖页面的反馈，可以为 nil。
func (s *Server) TriggerShot(done func(*lesson.ShotResult, error)) {
	go func() {
		res, err := s.runQuickShot()
		if err != nil {
			logx.Warnf("server", "识图失败: %v", err)
		}
		if done != nil {
			done(res, err)
		}
	}()
}

// handleOCRTest 截一张当前屏幕，直接打识别服务，验证地址与模型是否可用。
func (s *Server) handleOCRTest(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	cli := ocr.New(ocrConfig(cfg.OCR))
	if !cli.Enabled() {
		writeErr(w, 400, fmt.Errorf("画面识别未开启，先打开开关再测试"))
		return
	}
	img, err := capture.CaptureScreen()
	if err != nil {
		writeErr(w, 500, fmt.Errorf("截屏失败：%w", err))
		return
	}
	raw, err := capture.EncodeJPEG(img, 86)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("编码截图失败：%w", err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.OCR.TimeoutSec+10)*time.Second)
	defer cancel()
	start := time.Now()
	res, err := cli.Recognize(ctx, raw, "image/jpeg")
	if err != nil {
		logx.Warnf("ocr", "测试识别失败: %v", err)
		writeJSON(w, map[string]any{
			"ok": false, "desc": cli.Describe(), "error": err.Error(),
			"elapsedMs": int(time.Since(start).Milliseconds()),
		})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "desc": cli.Describe(), "text": res.Text, "chars": res.Chars,
		"elapsedMs": int(time.Since(start).Milliseconds()), "truncated": res.Truncated,
	})
}
