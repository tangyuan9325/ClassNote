package ocr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecognizeStringContent(t *testing.T) {
	var gotPrompt, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("路径应为 /chat/completions，实际 %s", r.URL.Path)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		for _, p := range body.Messages[0].Content {
			if p.Type == "text" {
				gotPrompt = p.Text
			}
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"# 标题\n\n- 第一条"}}]}`))
	}))
	defer srv.Close()

	c := New(Settings{Enabled: true, BaseURL: srv.URL, Model: "Unlimited-OCR"})
	res, err := c.Recognize(context.Background(), []byte("fake-jpeg"), "image/jpeg")
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if !strings.Contains(res.Text, "第一条") {
		t.Fatalf("文本未取到: %q", res.Text)
	}
	if gotModel != "Unlimited-OCR" {
		t.Fatalf("模型名应为 Unlimited-OCR，实际 %q", gotModel)
	}
	if gotPrompt != DefaultPrompt {
		t.Fatalf("默认提示词应为 %q，实际 %q", DefaultPrompt, gotPrompt)
	}
	if res.Chars == 0 || res.LatencyMS < 0 {
		t.Fatalf("结果字段异常: %+v", res)
	}
}

func TestRecognizePartsContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":[{"type":"text","text":"第一段"},{"type":"text","text":"第二段"}]}}]}`))
	}))
	defer srv.Close()

	c := New(Settings{Enabled: true, BaseURL: srv.URL})
	res, err := c.Recognize(context.Background(), []byte("x"), "")
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if res.Text != "第一段第二段" {
		t.Fatalf("分片内容拼接错误: %q", res.Text)
	}
}

func TestFriendlyConnectionError(t *testing.T) {
	// 起一个服务再立刻关掉，拿到一个必然拒绝连接的端口。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := New(Settings{Enabled: true, BaseURL: url})
	_, err := c.Recognize(context.Background(), []byte("x"), "")
	if err == nil {
		t.Fatal("应当返回连接错误")
	}
	if !strings.Contains(err.Error(), "没有启动") {
		t.Fatalf("错误提示不够直白: %v", err)
	}
}

func TestStatusError404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(Settings{Enabled: true, BaseURL: srv.URL})
	_, err := c.Recognize(context.Background(), []byte("x"), "")
	if err == nil || !strings.Contains(err.Error(), "/v1") {
		t.Fatalf("404 应提示地址格式，实际: %v", err)
	}
}

func TestCleanStripsFenceAndEcho(t *testing.T) {
	in := "```markdown\n<img>\\n第一行\n\n\n第二行\n```"
	out, _ := Clean(in, 0)
	if strings.Contains(out, "```") || strings.Contains(out, "<img>") {
		t.Fatalf("未脱壳: %q", out)
	}
	if !strings.Contains(out, "第一行") || !strings.Contains(out, "第二行") {
		t.Fatalf("正文丢失: %q", out)
	}
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("空行未压缩: %q", out)
	}

	if echo, _ := Clean("Document parsing.", 0); echo != "" {
		t.Fatalf("提示词回显应被丢弃，实际 %q", echo)
	}
}

func TestCleanTruncates(t *testing.T) {
	out, truncated := Clean(strings.Repeat("字", 30), 10)
	if !truncated || len([]rune(out)) != 11 { // 10 个字符 + 省略号
		t.Fatalf("截断不符: truncated=%v len=%d", truncated, len([]rune(out)))
	}
}

func TestEmptyImage(t *testing.T) {
	c := New(Settings{Enabled: true, BaseURL: "http://127.0.0.1:1/v1"})
	if _, err := c.Recognize(context.Background(), nil, ""); err == nil {
		t.Fatal("空图片应报错")
	}
}

func TestDescribeHidesKey(t *testing.T) {
	c := New(Settings{Enabled: true, BaseURL: "http://127.0.0.1:10000/v1", Model: "Unlimited-OCR", APIKey: "sk-secret"})
	d := c.Describe()
	if strings.Contains(d, "sk-secret") {
		t.Fatalf("描述里不应出现密钥: %s", d)
	}
	if !strings.Contains(d, "Unlimited-OCR") || !strings.Contains(d, "127.0.0.1:10000") {
		t.Fatalf("描述信息不全: %s", d)
	}
}
