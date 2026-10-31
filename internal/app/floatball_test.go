package app

import (
	"image"
	"testing"
)

// 悬浮球之前把中文画成统一的方块，肉眼看就是乱码。
// 用两个不同科目渲染，文字区域必须不同，才能证明真的画出了汉字。
func TestFloatBallRendersRealChinese(t *testing.T) {
	a := renderBall(fbAnimFrames, FloatBallStatus{Running: true, Subject: "语文", Elapsed: 65})
	b := renderBall(fbAnimFrames, FloatBallStatus{Running: true, Subject: "数学", Elapsed: 65})
	if sameRegion(a, b, 52, 16, 150, 35) {
		t.Fatal("不同科目渲染结果相同，中文仍是方块占位")
	}
}

// 英文小写也必须能画出来（旧点阵只有 C/N，ClassNote 会缺字）。
func TestFloatBallRendersLowercase(t *testing.T) {
	img := renderBall(fbAnimFrames, FloatBallStatus{Running: false})
	nonBg := 0
	for y := 36; y < 56; y++ {
		for x := 52; x < 170; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > 0x4000 || g > 0x4000 || b > 0x4000 {
				nonBg++
			}
		}
	}
	if nonBg < 40 {
		t.Fatalf("ClassNote 文字像素过少：%d", nonBg)
	}
}

func sameRegion(a, b *image.RGBA, x0, y0, x1, y1 int) bool {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				return false
			}
		}
	}
	return true
}
