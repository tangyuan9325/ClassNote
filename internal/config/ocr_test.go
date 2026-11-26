package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 1.2.12 的配置文件里没有 ocr 段，升级后必须自动补齐，且默认是「兜底」模式。
func TestLoadOldConfigFillsOCR(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	old := `{"version":1,"onboarded":true,"capture":{"hotkey":"Ctrl+Alt+S"}}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	c := s.Get()
	if !c.OCR.Enabled {
		t.Fatal("旧配置应补齐为默认开启")
	}
	if c.OCR.Mode != "fallback" {
		t.Fatalf("默认应为兜底模式，实际 %q", c.OCR.Mode)
	}
	if c.OCR.BaseURL != "http://127.0.0.1:10000/v1" {
		t.Fatalf("默认地址不对: %s", c.OCR.BaseURL)
	}
	if c.OCR.Model != "Unlimited-OCR" || c.OCR.Prompt != "document parsing." {
		t.Fatalf("默认模型/提示词不对: %+v", c.OCR)
	}
}

func TestNormalizeOCRBaseURL(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8000":           "http://127.0.0.1:8000/v1",
		"http://127.0.0.1:8000/":          "http://127.0.0.1:8000/v1",
		"http://127.0.0.1:10000/v1":       "http://127.0.0.1:10000/v1",
		"  https://ocr.example.com/v1/  ": "https://ocr.example.com/v1",
	}
	for in, want := range cases {
		c := Config{OCR: OCRConfig{BaseURL: in}}
		c.normalize()
		if c.OCR.BaseURL != want {
			t.Fatalf("%q → %q，应为 %q", in, c.OCR.BaseURL, want)
		}
	}
}

func TestNormalizeOCRMode(t *testing.T) {
	c := Config{OCR: OCRConfig{Mode: "always"}}
	c.normalize()
	if c.OCR.Mode != "always" {
		t.Fatal("always 应保留")
	}
	c2 := Config{OCR: OCRConfig{Mode: "sometimes"}}
	c2.normalize()
	if c2.OCR.Mode != "fallback" {
		t.Fatalf("未知模式应回落 fallback，实际 %q", c2.OCR.Mode)
	}
}
