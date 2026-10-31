// Command shot 用无头浏览器给页面截图。
//
//	go run ./cmd/shot <url> <out.png> [waitMs] [width] [height]
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"classnote/internal/llm"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: shot <url> <out.png> [waitMs] [w] [h]")
		return
	}
	url, out := os.Args[1], os.Args[2]
	wait := 2600
	w, h := 1380, 900
	if len(os.Args) > 3 {
		wait, _ = strconv.Atoi(os.Args[3])
	}
	if len(os.Args) > 5 {
		w, _ = strconv.Atoi(os.Args[4])
		h, _ = strconv.Atoi(os.Args[5])
	}
	proc, err := llm.LaunchBrowser(llm.BrowserBinary(), "", true)
	if err != nil {
		fmt.Println("launch:", err)
		return
	}
	defer func() {
		if proc.Cmd != nil && proc.Cmd.Process != nil {
			_ = proc.Cmd.Process.Kill()
		}
	}()
	br := rod.New().ControlURL(proc.WS)
	if err := br.Connect(); err != nil {
		fmt.Println("connect:", err)
		return
	}
	defer br.Close()
	page, err := br.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		fmt.Println("page:", err)
		return
	}
	_ = page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: w, Height: h, DeviceScaleFactor: 1})
	time.Sleep(time.Duration(wait) * time.Millisecond)
	raw, err := page.Screenshot(true, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		fmt.Println("shot:", err)
		return
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		fmt.Println("write:", err)
		return
	}
	fmt.Println("saved", out, len(raw))
}
