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

	files := orderedSlideFiles(&zr.Reader)
	if len(files) == 0 {
		return nil, fmt.Errorf("演示文稿中没有找到幻灯片")
	}

	deck := &Deck{Path: path, Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
	for _, f := range files {
		text, err := readSlideXML(f)
		if err != nil {
			deck.Slides = append(deck.Slides, "")
			continue
		}
		deck.Slides = append(deck.Slides, text)
	}
	return deck, nil
}

// orderedSlideFiles 按放映顺序返回幻灯片 XML。
//
// 页序必须以 ppt/presentation.xml 的 <p:sldIdLst> 为准，而不是 slideN.xml 的
// 文件名序号：教师在 PowerPoint 里拖动过顺序之后，slide7.xml 可能对应第 2 页，
// 按文件名排会把文字配到错误的页码上（报告里"文字与画面不符"多半是这个原因）。
// presentation.xml 读不到时才退回文件名排序。
func orderedSlideFiles(zr *zip.Reader) []*zip.File {
	byName := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	if order := slideOrderFromPresentation(zr); len(order) > 0 {
		out := make([]*zip.File, 0, len(order))
		for _, name := range order {
			if f, ok := byName[name]; ok {
				out = append(out, f)
			}
		}
		if len(out) == len(order) {
			return out
		}
	}
	// 回退：文件名序号。
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
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })
	out := make([]*zip.File, 0, len(slides))
	for _, s := range slides {
		out = append(out, s.file)
	}
	return out
}

// slideOrderFromPresentation 解析 sldIdLst + rels，得到 "ppt/slides/slideN.xml" 的有序列表。
func slideOrderFromPresentation(zr *zip.Reader) []string {
	pres := readZipByName(zr, "ppt/presentation.xml", 16<<20)
	rels := readZipByName(zr, "ppt/_rels/presentation.xml.rels", 8<<20)
	if pres == nil || rels == nil {
		return nil
	}
	var presDoc struct {
		IDs []struct {
			RID string `xml:"id,attr"`
		} `xml:"sldIdLst>sldId"`
	}
	if err := xml.Unmarshal(pres, &presDoc); err != nil || len(presDoc.IDs) == 0 {
		return nil
	}
	var relDoc struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(rels, &relDoc); err != nil {
		return nil
	}
	targets := make(map[string]string, len(relDoc.Rels))
	for _, r := range relDoc.Rels {
		targets[r.ID] = r.Target
	}
	out := make([]string, 0, len(presDoc.IDs))
	for _, id := range presDoc.IDs {
		t, ok := targets[id.RID]
		if !ok {
			return nil
		}
		t = strings.TrimPrefix(t, "/")
		if !strings.HasPrefix(t, "ppt/") {
			t = "ppt/" + strings.TrimPrefix(t, "./")
		}
		out = append(out, t)
	}
	return out
}

func readZipByName(zr *zip.Reader, name string, limit int64) []byte {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		raw, err := io.ReadAll(io.LimitReader(rc, limit))
		if err != nil {
			return nil
		}
		return raw
	}
	return nil
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
