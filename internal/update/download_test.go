package update

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeZip 生成一个最小可用的 zip，放在临时目录里。
func makeZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("写 zip 失败: %v", err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("写 zip 失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	p := filepath.Join(t.TempDir(), "pkg.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fileSrv 起一个支持 Range 的文件服务。
func fileSrv(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "pkg.zip", time.Now(), bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// waitState 等到任务进入终态。
func waitState(t *testing.T, d *Downloader, want ...string) Progress {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		p := d.Current()
		for _, w := range want {
			if p.State == w {
				return p
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待状态 %v 超时，当前 %+v", want, d.Current())
	return Progress{}
}

func TestDownloadSuccessAndProgress(t *testing.T) {
	body := makeZip(t, map[string][]byte{"ClassNote.exe": bytes.Repeat([]byte("M"), 4096)})
	raw, _ := os.ReadFile(body)
	srv := fileSrv(t, raw)

	var mu sync.Mutex
	var seen []string
	d := NewDownloader(func(p Progress) {
		mu.Lock()
		seen = append(seen, p.State)
		mu.Unlock()
	})
	dir := t.TempDir()
	if err := d.Start(Job{
		URL:  "https://github.com/a/b/releases/download/v1/pkg.zip",
		Name: "pkg.zip", Dir: dir, Expect: int64(len(raw)),
		Endpoints: []Endpoint{{ID: "e1", Name: "e1", URL: srv.URL + "/pkg.zip"}},
	}); err != nil {
		t.Fatal(err)
	}
	p := waitState(t, d, "done", "error")
	if p.State != "done" {
		t.Fatalf("下载失败：%+v", p)
	}
	if p.Path != filepath.Join(dir, "pkg.zip") {
		t.Fatalf("落地路径错误：%s", p.Path)
	}
	if p.Total != int64(len(raw)) || p.Done != int64(len(raw)) {
		t.Fatalf("进度数字不对：%+v", p)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[0] != "downloading" {
		t.Fatalf("应看到 downloading 状态，实际 %v", seen)
	}
	if _, err := os.Stat(p.Path + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part 应该在完成后被改名，不该残留")
	}
}

func TestDownloadResumesFromPart(t *testing.T) {
	files := map[string][]byte{"ClassNote.exe": bytes.Repeat([]byte("Z"), 64<<10)}
	zipPath := makeZip(t, files)
	raw, _ := os.ReadFile(zipPath)
	half := len(raw) / 2

	dir := t.TempDir()
	part := filepath.Join(dir, "pkg.zip.part")
	if err := os.WriteFile(part, raw[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	var gotRange string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		http.ServeContent(w, r, "pkg.zip", time.Now(), bytes.NewReader(raw))
	}))
	defer proxy.Close()

	d := NewDownloader(nil)
	if err := d.Start(Job{
		URL: "https://github.com/a/b/pkg.zip", Name: "pkg.zip", Dir: dir,
		Expect:    int64(len(raw)),
		Endpoints: []Endpoint{{ID: "e", Name: "e", URL: proxy.URL + "/pkg.zip"}},
	}); err != nil {
		t.Fatal(err)
	}
	p := waitState(t, d, "done", "error")
	if p.State != "done" {
		t.Fatalf("续传失败：%+v", p)
	}
	if gotRange != "bytes="+strconv.Itoa(half)+"-" {
		t.Fatalf("续传应带 Range 头，实际 %q", gotRange)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "pkg.zip"))
	if !bytes.Equal(got, raw) {
		t.Fatal("续传后文件内容不一致")
	}
}

func TestDownloadFallsBackToSecondNode(t *testing.T) {
	zipPath := makeZip(t, map[string][]byte{"ClassNote.exe": bytes.Repeat([]byte("A"), 32<<10)})
	raw, _ := os.ReadFile(zipPath)
	good := fileSrv(t, raw)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota exceeded", http.StatusForbidden)
	}))
	defer broken.Close()

	d := NewDownloader(nil)
	dir := t.TempDir()
	if err := d.Start(Job{
		URL: "https://github.com/a/b/pkg.zip", Name: "pkg.zip", Dir: dir,
		Endpoints: []Endpoint{
			{ID: "bad", Name: "bad", URL: broken.URL + "/pkg.zip"},
			{ID: "good", Name: "good", URL: good.URL + "/pkg.zip"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	p := waitState(t, d, "done", "error")
	if p.State != "done" {
		t.Fatalf("第二个节点应成功，实际 %+v", p)
	}
	if p.NodeID != "good" {
		t.Fatalf("最终节点应是 good，实际 %s", p.NodeID)
	}
}

// TestDownloadRejectsHTMLPage 覆盖反代的经典坑：限流时返回 200 + HTML 错误页。
func TestDownloadRejectsHTMLPage(t *testing.T) {
	htmlPage := []byte("<html><body>Rate limit exceeded, please retry later</body></html>")
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(htmlPage)
	}))
	defer fake.Close()

	d := NewDownloader(nil)
	dir := t.TempDir()
	if err := d.Start(Job{
		URL: "https://github.com/a/b/pkg.zip", Name: "pkg.zip", Dir: dir,
		Endpoints: []Endpoint{{ID: "html", Name: "html", URL: fake.URL + "/pkg.zip"}},
	}); err != nil {
		t.Fatal(err)
	}
	p := waitState(t, d, "done", "error")
	if p.State != "error" {
		t.Fatalf("HTML 错误页必须判为失败，实际 %+v", p)
	}
	if !strings.Contains(p.Error, "zip") {
		t.Fatalf("失败原因应说明不是有效 zip，实际 %q", p.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg.zip")); !os.IsNotExist(err) {
		t.Fatal("坏包不该留在落地路径上")
	}
}

func TestDownloadAllNodesFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()
	d := NewDownloader(nil)
	if err := d.Start(Job{
		URL: "https://github.com/a/b/pkg.zip", Name: "pkg.zip", Dir: t.TempDir(),
		Endpoints: []Endpoint{{ID: "x", Name: "x", URL: srv.URL}},
	}); err != nil {
		t.Fatal(err)
	}
	p := waitState(t, d, "done", "error")
	if p.State != "error" || !strings.Contains(p.Error, "所有下载节点") {
		t.Fatalf("应报全部节点失败，实际 %+v", p)
	}
}

func TestDownloadRejectsBadName(t *testing.T) {
	zipPath := makeZip(t, map[string][]byte{"whatever.txt": []byte("x")})
	raw, _ := os.ReadFile(zipPath)
	srv := fileSrv(t, raw)
	d := NewDownloader(nil)
	dir := t.TempDir()
	// 穿越路径必须被挡在目录之外。
	if err := d.Start(Job{
		URL: "https://x/y.zip", Name: "../../evil.zip", Dir: dir,
		Endpoints: []Endpoint{{ID: "e", Name: "e", URL: srv.URL + "/y.zip"}},
	}); err != nil {
		t.Fatal(err)
	}
	p := waitState(t, d, "done", "error")
	if p.State != "done" {
		t.Fatalf("下载应成功：%+v", p)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.zip")); !os.IsNotExist(err) {
		t.Fatal("文件名穿越没被拦住")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.zip")); err != nil {
		t.Fatalf("文件应落在下载目录内：%v", err)
	}
}

func TestVerifyZip(t *testing.T) {
	good := makeZip(t, map[string][]byte{"a.txt": []byte("hello")})
	if err := verifyZip(good); err != nil {
		t.Fatalf("正常包应通过：%v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.zip")
	_ = os.WriteFile(bad, bytes.Repeat([]byte("x"), 4096), 0o644)
	if err := verifyZip(bad); err == nil {
		t.Fatal("垃圾数据应判为无效 zip")
	}
	tiny := filepath.Join(t.TempDir(), "tiny.zip")
	_ = os.WriteFile(tiny, []byte("PK"), 0o644)
	if err := verifyZip(tiny); err == nil {
		t.Fatal("过小的文件应判为无效")
	}
}

func TestResumeOffsetDropsOversizedPart(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "x.part")
	_ = os.WriteFile(part, bytes.Repeat([]byte("a"), 100), 0o644)
	if got := resumeOffset(part, 50); got != 0 {
		t.Fatalf("比远端还大的缓存应清零，实际 %d", got)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal("坏缓存应被删除")
	}
	if got := resumeOffset(part, 0); got != 0 {
		t.Fatalf("没有缓存时应返回 0，实际 %d", got)
	}
}
