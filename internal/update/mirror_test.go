package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWrapAndHost(t *testing.T) {
	direct := Node{ID: "direct", Prefix: ""}
	if got := direct.Wrap("https://github.com/a/b.zip"); got != "https://github.com/a/b.zip" {
		t.Fatalf("直连不该改写地址，得到 %s", got)
	}
	if direct.Host() != "github.com" {
		t.Fatalf("直连主机名错误：%s", direct.Host())
	}
	n := Node{ID: "ghfast", Prefix: "https://ghfast.top/"}
	if got := n.Wrap("https://github.com/a/b.zip"); got != "https://ghfast.top/https://github.com/a/b.zip" {
		t.Fatalf("节点前缀拼接错误：%s", got)
	}
	if n.Host() != "ghfast.top" {
		t.Fatalf("节点主机名错误：%s", n.Host())
	}
}

func TestCandidatesCustomValidation(t *testing.T) {
	got := Candidates([]string{
		"https://my.proxy/",   // 合法
		"http://plain.proxy/", // 非 https，必须丢弃
		"https://bad.proxy/?a=1",
		"", // 空
	})
	var customs []Node
	for _, n := range got {
		if n.Custom {
			customs = append(customs, n)
		}
	}
	if len(customs) != 1 {
		t.Fatalf("自定义节点应只留 1 个，实际 %d 个：%+v", len(customs), customs)
	}
	if customs[0].Prefix != "https://my.proxy/" {
		t.Fatalf("自定义前缀被改写错了：%s", customs[0].Prefix)
	}
	if len(got) <= len(customs) {
		t.Fatal("内置节点丢失")
	}
}

func TestValidPrefix(t *testing.T) {
	cases := map[string]bool{
		"https://ghfast.top/":    true,
		"https://a.b/c/":         true,
		"http://ghfast.top/":     false,
		"ftp://ghfast.top/":      false,
		"https://":               false,
		"https://a b/":           false,
		"https://a.b/?x=1":       false,
		"file:///etc/passwd":     false,
		"https://127.0.0.1:8080": true,
	}
	for in, want := range cases {
		if got := validPrefix(in); got != want {
			t.Errorf("validPrefix(%q)=%v，期望 %v", in, got, want)
		}
	}
}

func TestTotalFromRange(t *testing.T) {
	cases := map[string]int64{
		"bytes 0-1048575/80289949": 80289949,
		"bytes 0-1/*":              0,
		"":                         0,
		"bytes 0-1":                0,
	}
	for in, want := range cases {
		if got := totalFromRange(in); got != want {
			t.Errorf("totalFromRange(%q)=%d，期望 %d", in, got, want)
		}
	}
}

func TestRankNodes(t *testing.T) {
	nodes := []Node{
		{Name: "dead", OK: false, Tested: true, Error: "HTTP 404"},
		{Name: "slow", OK: true, Speed: 100 << 10, Latency: 100},
		{Name: "fast", OK: true, Speed: 5 << 20, Latency: 400},
		{Name: "same-speed-low-latency", OK: true, Speed: 5 << 20, Latency: 120},
	}
	got := rankNodes(nodes)
	if got[0].Name != "same-speed-low-latency" {
		t.Fatalf("速度相同时应选延迟低的，实际第一是 %s", got[0].Name)
	}
	if got[1].Name != "fast" {
		t.Fatalf("第二应是 fast，实际 %s", got[1].Name)
	}
	if got[len(got)-1].OK {
		t.Fatalf("不可用节点应排最后，实际 %s", got[len(got)-1].Name)
	}
	if !got[len(got)-1].Tested || got[len(got)-1].Error == "" {
		t.Fatal("不可用节点应保留失败原因")
	}
}

// TestTestNodesAgainstServers 用本地假节点验证「并发测速 + 排序」。
func TestTestNodesAgainstServers(t *testing.T) {
	body := make([]byte, 512<<10)
	for i := range body {
		body[i] = byte(i)
	}
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.zip", time.Now(), strings.NewReader(string(body)))
	}))
	defer fast.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer broken.Close()

	nodes := []Node{
		{ID: "broken", Name: "broken", Prefix: broken.URL + "/"},
		{ID: "fast", Name: "fast", Prefix: fast.URL + "/"},
	}
	// TestNodes 只认内置 + 自定义前缀；这里直接调 probeOne 更直接。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bad := probeOne(ctx, nodes[0], "https://github.com/a/b.zip")
	if bad.OK {
		t.Fatal("403 的节点不该判为可用")
	}
	if !strings.Contains(bad.Error, "403") {
		t.Fatalf("失败原因应包含状态码，实际 %q", bad.Error)
	}
	good := probeOne(ctx, nodes[1], "https://github.com/a/b.zip")
	if !good.OK {
		t.Fatalf("正常节点应可用，实际 %q", good.Error)
	}
	if good.Speed <= 0 || good.Latency < 0 {
		t.Fatalf("测速数据不合理：speed=%d latency=%d", good.Speed, good.Latency)
	}
	if good.Size != int64(len(body)) {
		t.Fatalf("总大小解析错误：%d", good.Size)
	}
}

// TestTestNodesCaches 确认同一地址不会每次都重新测速。
func TestTestNodesCaches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hi"))
	}))
	defer srv.Close()
	raw := "https://github.com/example/cache-test/releases/download/v1/x.zip"
	storeCache(raw, false, []Node{{ID: "cached", Name: "cached", OK: true, Speed: 12345}})
	got := TestNodes(context.Background(), ProbeReq{URL: raw})
	if len(got) != 1 || got[0].ID != "cached" {
		t.Fatalf("应命中缓存，实际 %+v", got)
	}
	// 清理，避免影响其它用例。
	cacheMu.Lock()
	nodeCache = map[string]cacheEntry{}
	cacheMu.Unlock()
}

func TestCheckReportsMissingRepo(t *testing.T) {
	info := Check("", "")
	if info.Error == "" || info.HasUpdate {
		t.Fatalf("空仓库应直接报错，实际 %+v", info)
	}
}

func TestNewerAndSplitVer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.8.2", "1.8.1", true},
		{"1.8.1", "1.8.1", false},
		{"1.2.6.1", "1.2.6", true},
		{"v1.10.0", "1.9.9", true},
		{"1.8.1-beta", "1.8.1", false},
	}
	for _, c := range cases {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q,%q)=%v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}

func TestStripHTML(t *testing.T) {
	in := "<h1>标题</h1>\n<p>第一段<br/>第二行</p>\n\n\n<p>结尾</p>"
	got := stripHTML(in)
	if strings.ContainsAny(got, "<>") {
		t.Fatalf("标签没清干净：%q", got)
	}
	if !strings.Contains(got, "标题") || !strings.Contains(got, "结尾") {
		t.Fatalf("正文丢了：%q", got)
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("空行没压缩：%q", got)
	}
}

func TestVersionFromHref(t *testing.T) {
	cases := map[string]string{
		"https://github.com/a/b/releases/tag/v1.8.1": "1.8.1",
		"https://github.com/a/b/releases/tag/1.2.3":  "1.2.3",
		"https://github.com/a/b":                     "",
	}
	for in, want := range cases {
		if got := versionFromHref(in); got != want {
			t.Errorf("versionFromHref(%q)=%q，期望 %q", in, got, want)
		}
	}
}

func TestWriteProbeReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 无视目录权限，跳过大权限用例")
	}
	dir := t.TempDir()
	if err := os.Chmod(filepath.Join(dir), 0o500); err != nil {
		t.Skip("无法改权限")
	}
	if err := writeProbe(dir); err == nil {
		t.Fatal("只读目录应探测失败")
	}
}
