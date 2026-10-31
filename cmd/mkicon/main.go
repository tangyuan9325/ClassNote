// mkicon 生成 ClassNote 品牌图标：橙色圆角方块 + 白色笔记页（折角 + 三条横线），
// 与托盘图标（internal/app/assets/tray.ico）同款视觉。
//
//	go run ./cmd/mkicon -out app.ico
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

var (
	brand = color.RGBA{0xC9, 0x64, 0x42, 0xFF} // 赤陶橙
	cream = color.RGBA{0xFA, 0xF9, 0xF5, 0xFF} // 纸白
	sizes = []int{16, 24, 32, 48, 64, 128, 256}
)

func main() {
	out := flag.String("out", "app.ico", "输出 .ico 路径")
	flag.Parse()

	type entry struct {
		size int
		png  []byte
	}
	var entries []entry
	for _, s := range sizes {
		img := render(s)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		entries = append(entries, entry{s, buf.Bytes()})
	}

	var ico bytes.Buffer
	_ = binary.Write(&ico, binary.LittleEndian, uint16(0))
	_ = binary.Write(&ico, binary.LittleEndian, uint16(1))
	_ = binary.Write(&ico, binary.LittleEndian, uint16(len(entries)))
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		b := byte(e.size)
		if e.size >= 256 {
			b = 0
		}
		_ = ico.WriteByte(b) // width（0 表示 256）
		_ = ico.WriteByte(b) // height
		_ = ico.WriteByte(0) // palette
		_ = ico.WriteByte(0) // reserved
		_ = binary.Write(&ico, binary.LittleEndian, uint16(1))  // planes
		_ = binary.Write(&ico, binary.LittleEndian, uint16(32)) // bpp
		_ = binary.Write(&ico, binary.LittleEndian, uint32(len(e.png)))
		_ = binary.Write(&ico, binary.LittleEndian, uint32(offset))
		offset += len(e.png)
	}
	for _, e := range entries {
		ico.Write(e.png)
	}
	if err := os.WriteFile(*out, ico.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("written", *out, ico.Len(), "bytes")
}

// render 画 size×size 品牌图。先按 4 倍超采样再缩小抗锯齿。
func render(size int) *image.RGBA {
	const ss = 4
	hi := drawBrand(size * ss)
	return downsample(hi, size)
}

// drawBrand 画品牌图案（在 s×s 画布上）：
// 1) 圆角方块（赤陶橙） 2) 白色笔记页（右上折角） 3) 页上三条橙色横线。
func drawBrand(s int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	f := float64(s)

	// 1) 圆角方块
	r := f * 0.22
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			if insideRounded(float64(x)+0.5, float64(y)+0.5, f, r) {
				img.SetRGBA(x, y, brand)
			}
		}
	}

	// 2) 笔记页：居中偏上，圆角矩形 + 右上折角
	px0, py0 := f*0.26, f*0.16
	px1, py1 := f*0.74, f*0.86
	pr := f * 0.05 // 纸张小圆角
	fold := f * 0.15

	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if fx < px0 || fx > px1 || fy < py0 || fy > py1 {
				continue
			}
			// 折角缺口：右上角一条对角线切掉（露出底色）
			if fx > px1-fold && fy < py0+fold {
				// 对角线方程：(fx-(px1-fold))/fold + (fy-py0)/fold < 1 → 缺口
				if (fx-(px1-fold))/fold+(fy-py0)/fold < 1.0 {
					continue
				}
			}
			if insideRoundedRect(fx-px0, fy-py0, px1-px0, py1-py0, pr) {
				img.SetRGBA(x, y, cream)
			}
		}
	}
	// 折角小三角（略暗的纸色，表示翻折）
	dim := color.RGBA{0xE8, 0xE2, 0xD6, 0xFF}
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if fx > px1-fold && fy < py0+fold {
				if (fx-(px1-fold))/fold+(fy-py0)/fold >= 1.0 && fy <= py0+fold*0.55 && fx <= px1-fold*0.45 {
					// 折起的小三角：上边缘一条
					if (fx-(px1-fold))/fold+(fy-py0)/fold < 1.45 {
						img.SetRGBA(x, y, dim)
					}
				}
			}
		}
	}

	// 3) 三条横线（圆头），长度递减
	lines := []struct{ y, w float64 }{
		{f*0.36, f*0.30},
		{f*0.50, f*0.24},
		{f*0.64, f*0.17},
	}
	lh := f * 0.055
	lx := f * 0.335
	for _, ln := range lines {
		for y := 0; y < s; y++ {
			for x := 0; x < s; x++ {
				fx, fy := float64(x)+0.5, float64(y)+0.5
				if inRoundBar(fx, fy, lx, ln.y, ln.w, lh) {
					img.SetRGBA(x, y, brand)
				}
			}
		}
	}
	return img
}

// inRoundBar 判断点是否在水平圆头长条内（中心线 y=cy，长 w，粗 h）。
func inRoundBar(x, y, x0, cy, w, h float64) bool {
	hh := h / 2
	r := hh
	if x >= x0+r && x <= x0+w-r {
		return math.Abs(y-cy) <= hh
	}
	// 两端半圆
	cx := x0 + r
	if x < x0+r {
		return math.Pow(x-cx, 2)+math.Pow(y-cy, 2) <= r*r
	}
	cx = x0 + w - r
	return math.Pow(x-cx, 2)+math.Pow(y-cy, 2) <= r*r
}

// insideRounded 判断点（局部坐标 0..s）是否在圆角正方形内。
func insideRounded(x, y, s, r float64) bool {
	return insideRoundedRect(x, y, s, s, r)
}

// insideRoundedRect 判断点（局部坐标 0..w, 0..h）是否在圆角矩形内。
func insideRoundedRect(x, y, w, h, r float64) bool {
	if x < 0 || y < 0 || x > w || y > h {
		return false
	}
	if x < r && y < r {
		return math.Pow(x-r, 2)+math.Pow(y-r, 2) <= r*r
	}
	if x > w-r && y < r {
		return math.Pow(x-(w-r), 2)+math.Pow(y-r, 2) <= r*r
	}
	if x < r && y > h-r {
		return math.Pow(x-r, 2)+math.Pow(y-(h-r), 2) <= r*r
	}
	if x > w-r && y > h-r {
		return math.Pow(x-(w-r), 2)+math.Pow(y-(h-r), 2) <= r*r
	}
	return true
}

// downsample 把大图按块平均缩小（抗锯齿）。
func downsample(src *image.RGBA, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	n := src.Bounds().Dx() / size
	if n < 1 {
		n = 1
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a int
			for dy := 0; dy < n; dy++ {
				for dx := 0; dx < n; dx++ {
					c := src.RGBAAt(x*n+dx, y*n+dy)
					r += int(c.R)
					g += int(c.G)
					b += int(c.B)
					a += int(c.A)
				}
			}
			t := n * n
			dst.SetRGBA(x, y, color.RGBA{uint8(r / t), uint8(g / t), uint8(b / t), uint8(a / t)})
		}
	}
	return dst
}
