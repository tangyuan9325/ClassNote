package capture

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Deck 是一份演示文稿的文本内容。
type Deck struct {
	Path   string   `json:"path"`
	Title  string   `json:"title"`
	Slides []string `json:"slides"` // 下标 0 对应第 1 页
}

// SlideText 返回指定页码（1 起）的文字。
func (d *Deck) SlideText(index int) string {
	if d == nil || index <= 0 || index > len(d.Slides) {
		return ""
	}
	return d.Slides[index-1]
}

// ExtractDeck 从 .pptx 中提取每页文字。
// PPTX 本质是 zip + XML，直接读 ppt/slides/slideN.xml 里的 <a:t> 节点，
// 比截图 OCR 更准确，也不需要额外的图像识别依赖。
func ExtractDeck(path string) (*Deck, error) {
	if !strings.EqualFold(filepath.Ext(path), ".pptx") {
		return nil, fmt.Errorf("仅支持 .pptx（当前文件：%s）", filepath.Base(path))
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("打开演示文稿失败: %w", err)
	}
	defer zr.Close()

	type slideFile struct {
		num  int
		file *zip.File
	}
	var slides []slideFile
	for _, f := range zr.File {
		name := f.Name
		if !strings.HasPrefix(name, "ppt/slides/slide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		base := strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml")
		n, err := strconv.Atoi(base)
		if err != nil {
			continue
		}
		slides = append(slides, slideFile{num: n, file: f})
	}
	if len(slides) == 0 {
		return nil, fmt.Errorf("演示文稿中没有找到幻灯片")
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })

	deck := &Deck{Path: path, Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
	for _, s := range slides {
		text, err := readSlideXML(s.file)
		if err != nil {
			deck.Slides = append(deck.Slides, "")
			continue
		}
		deck.Slides = append(deck.Slides, text)
	}
	return deck, nil
}

func readSlideXML(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 8<<20))
	if err != nil {
		return "", err
	}
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	var parts []string
	var inText bool
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			// a:t 是文本节点；a:br 表示换行。
			if t.Name.Local == "t" {
				inText = true
			} else if t.Name.Local == "br" {
				parts = append(parts, "\n")
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
		case xml.CharData:
			if inText {
				parts = append(parts, string(t))
			}
		}
	}
	text := strings.Join(parts, "")
	// 合并多余空行，去掉首尾空白。
	lines := strings.Split(text, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, " / "), nil
}
