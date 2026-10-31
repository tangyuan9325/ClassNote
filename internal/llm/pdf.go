package llm

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// RenderPDF 用浏览器把本地 HTML 渲染成 PDF。
// 走 CDP 的 Page.printToPDF，比命令行 --print-to-pdf 更可靠（后者在部分 Edge 版本上
// 会静默失败，既不报错也不产出文件）。
func RenderPDF(htmlPath, outPath, profileDir string) error {
	abs, err := filepath.Abs(htmlPath)
	if err != nil {
		return err
	}
	if profileDir == "" {
		profileDir = filepath.Join(os.TempDir(), "classnote-pdfprofile")
	}
	proc, err := LaunchBrowser("", profileDir, true)
	if err != nil {
		return err
	}
	if proc.Cmd != nil && proc.Cmd.Process != nil {
		defer func() { _ = proc.Cmd.Process.Kill() }()
	}
	br := rod.New().ControlURL(proc.WS)
	if err := br.Connect(); err != nil {
		return fmt.Errorf("连接浏览器失败: %w", err)
	}
	defer br.Close()

	u := "file:///" + strings.ReplaceAll(strings.TrimPrefix(filepath.ToSlash(abs), "/"), " ", "%20")
	page, err := br.Page(proto.TargetCreateTarget{URL: u})
	if err != nil {
		return fmt.Errorf("打开报告页面失败: %w", err)
	}
	defer page.Close()
	page = page.Timeout(60 * time.Second)
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("报告页面加载超时: %w", err)
	}
	// 等待内联图片与字体就绪。
	time.Sleep(1200 * time.Millisecond)
	if _, err := page.Eval(`() => new Promise(r => {
		const imgs = [...document.images];
		if (!imgs.length) return r(true);
		let left = imgs.length;
		imgs.forEach(i => { if (i.complete) { left--; } else { i.onload = i.onerror = () => { if (--left === 0) r(true); }; } });
		if (left === 0) r(true);
		setTimeout(() => r(true), 4000);
	})`); err != nil {
		_ = err
	}

	stream, err := page.PDF(&proto.PagePrintToPDF{
		PrintBackground: true,
		PaperWidth:      ptrFloat(8.27),
		PaperHeight:     ptrFloat(11.69),
		MarginTop:       ptrFloat(0.35),
		MarginBottom:    ptrFloat(0.35),
		MarginLeft:      ptrFloat(0.3),
		MarginRight:     ptrFloat(0.3),
	})
	if err != nil {
		return fmt.Errorf("生成 PDF 失败: %w", err)
	}
	raw, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("读取 PDF 数据失败: %w", err)
	}
	if len(raw) < 1000 {
		return fmt.Errorf("PDF 内容异常（%d 字节）", len(raw))
	}
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		return err
	}
	return nil
}

func ptrFloat(v float64) *float64 { return &v }
