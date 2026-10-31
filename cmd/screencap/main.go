// screencap 抓取屏幕区域：screencap <out.png> [l t r b]
package main

import (
	"fmt"
	"os"
	"strconv"

	"classnote/internal/capture"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: screencap out.png [l t r b]")
		return
	}
	capture.InitDPI()
	var r capture.Rect
	if len(os.Args) >= 6 {
		n := func(i int) int { v, _ := strconv.Atoi(os.Args[i]); return v }
		r = capture.Rect{Left: int32(n(2)), Top: int32(n(3)), Right: int32(n(4)), Bottom: int32(n(5))}
	} else {
		w, h := capture.ScreenSize()
		r = capture.Rect{Left: 0, Top: 0, Right: int32(w), Bottom: int32(h)}
	}
	img, err := capture.CaptureRect(r)
	if err != nil {
		fmt.Println("capture:", err)
		return
	}
	if err := capture.SaveImage(img, os.Args[1], 92); err != nil {
		fmt.Println("save:", err)
		return
	}
	fmt.Println("saved", os.Args[1])
}
