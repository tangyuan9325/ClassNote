package asrmodel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeModel 造一个体积可控的假模型，替换全局清单后测下载链路。
//
// 不真下 60 MB：单测要秒级完成，而且不能依赖外网。
func withFakeModel(t *testing.T, body []byte) Model {
	t.Helper()
	sum, err := sha256OfBytes(body)
	if err != nil {
		t.Fatal(err)
	}
	m := Model{
		ID: "fake", File: "ggml-fake.bin", Name: "Fake",
		Size: int64(len(body)), SHA256: sum,
	}
	old := Models
	t.Cleanup(func() { Models = old })
	Models = []Model{m}
	return m
}

func sha256OfBytes(b []byte) (string, error) {
	dir, err := os.MkdirTemp("", "sum-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "x")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return fileSHA256(p)
}

func newTestMgr(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	SetRuntimeDir(dir)
	t.Cleanup(func() { SetRuntimeDir("") })
	m := NewManager(func(p Progress) {})
	return m, dir
}

// TestDownloadOK 正常下载 + 校验通过 + .part 被改名。
func TestDownloadOK(t *testing.T) {
	body := []byte(strings.Repeat("ggml-model-bytes-", 8192))
	m := withFakeModel(t, body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	mgr, dir := newTestMgr(t)
	if err := mgr.Start(m.ID, srv.URL); err != nil {
		t.Fatalf("启动下载失败：%v", err)
	}
	if !waitDone(t, mgr, 10*time.Second) {
		t.Fatalf("下载没有完成，最后状态 %+v", mgr.Current())
	}
	got, err := os.ReadFile(filepath.Join(dir, "models", m.File))
	if err != nil {
		t.Fatalf("目标文件不存在：%v", err)
	}
	if string(got) != string(body) {
		t.Fatal("文件内容与源不一致")
	}
	if _, err := os.Stat(filepath.Join(dir, "models", m.File+".part")); err == nil {
		t.Fatal(".part 没有清理")
	}
	if !HasModel(m) {
		t.Fatal("HasModel 应为 true")
	}
}

// TestResumeFromPart 已经下了一半的文件必须续传，而不是从头再来。
func TestResumeFromPart(t *testing.T) {
	body := []byte(strings.Repeat("ABCDEFGH", 16384))
	m := withFakeModel(t, body)
	var gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		http.ServeContent(w, r, m.File, time.Now(), strings.NewReader(string(body)))
	}))
	defer srv.Close()

	mgr, dir := newTestMgr(t)
	if err := os.MkdirAll(filepath.Join(dir, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	half := len(body) / 2
	if err := os.WriteFile(filepath.Join(dir, "models", m.File+".part"), body[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Start(m.ID, srv.URL); err != nil {
		t.Fatalf("启动下载失败：%v", err)
	}
	if !waitDone(t, mgr, 10*time.Second) {
		t.Fatalf("续传没有完成：%+v", mgr.Current())
	}
	if gotRange != "bytes=131072-" && gotRange == "" {
		t.Fatalf("没有带 Range 头，说明从头重下了：%q", gotRange)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "models", m.File))
	if string(got) != string(body) {
		t.Fatalf("续传后内容不对（长度 %d，应为 %d）", len(got), len(body))
	}
}

// TestChecksumRetry 第一次给坏数据、第二次给好数据时，应自动重下成功。
func TestChecksumRetry(t *testing.T) {
	body := []byte(strings.Repeat("0123456789", 4096))
	m := withFakeModel(t, body)
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		payload := body
		if first {
			payload = []byte(strings.Repeat("X", len(body)))
		}
		w.Header().Set("Content-Length", itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	mgr, _ := newTestMgr(t)
	if err := mgr.Start(m.ID, srv.URL); err != nil {
		t.Fatalf("启动下载失败：%v", err)
	}
	if !waitDone(t, mgr, 15*time.Second) {
		t.Fatalf("校验失败后应自动重下，实际：%+v", mgr.Current())
	}
	mu.Lock()
	defer mu.Unlock()
	if n < 2 {
		t.Fatalf("只请求了 %d 次，没有触发重下", n)
	}
}

// TestCancel 取消后保留 .part，状态是 canceled 而不是 error。
func TestCancel(t *testing.T) {
	body := make([]byte, 4<<20)
	m := withFakeModel(t, body)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(200)
		for i := 0; i < 8; i++ {
			_, _ = w.Write(body[i*(len(body)/8) : (i+1)*(len(body)/8)])
			w.(http.Flusher).Flush()
			select {
			case <-release:
				return
			case <-time.After(80 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()
	defer close(release)

	mgr, dir := newTestMgr(t)
	if err := mgr.Start(m.ID, srv.URL); err != nil {
		t.Fatalf("启动下载失败：%v", err)
	}
	time.Sleep(250 * time.Millisecond)
	mgr.Cancel(m.ID)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.Current().State == "canceled" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s := mgr.Current().State; s != "canceled" {
		t.Fatalf("取消后状态应为 canceled，实际 %s", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "models", m.File+".part")); err != nil {
		t.Fatal("取消后应保留 .part 以便续传")
	}
}

// TestSourceFallback 第一个源 500 时要自动换下一个。
func TestSourceFallback(t *testing.T) {
	body := []byte(strings.Repeat("fallback-", 4096))
	m := withFakeModel(t, body)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer good.Close()

	mgr, _ := newTestMgr(t)
	// 把两个源都塞进去：先坏后好。
	old := defaultSources
	defaultSources = []string{bad.URL + "/", good.URL + "/"}
	t.Cleanup(func() { defaultSources = old })

	if err := mgr.Start(m.ID, ""); err != nil {
		t.Fatalf("启动下载失败：%v", err)
	}
	if !waitDone(t, mgr, 10*time.Second) {
		t.Fatalf("应该回落到第二个源并成功：%+v", mgr.Current())
	}
}

// TestRejectUnknownModel 空 ID 与未知 ID 都要被挡。
func TestRejectUnknownModel(t *testing.T) {
	mgr, _ := newTestMgr(t)
	if err := mgr.Start("", ""); err == nil {
		t.Fatal("空 ID 应报错")
	}
	if err := mgr.Start("../../../etc/passwd", ""); err == nil {
		t.Fatal("路径样式的 ID 应报错")
	}
}

// TestModelListSane 清单本身要自洽：文件名为纯 ASCII、体积为正、sha256 长度对。
func TestModelListSane(t *testing.T) {
	if len(Models) < 3 {
		t.Fatalf("模型太少：%d", len(Models))
	}
	seen := map[string]bool{}
	for _, m := range Models {
		if seen[m.ID] {
			t.Fatalf("重复的模型 ID：%s", m.ID)
		}
		seen[m.ID] = true
		if m.Size <= 0 {
			t.Fatalf("%s 体积异常：%d", m.ID, m.Size)
		}
		if len(m.SHA256) != 64 {
			t.Fatalf("%s 的 sha256 长度不对：%d", m.ID, len(m.SHA256))
		}
		if !strings.HasPrefix(m.File, "ggml-") || !strings.HasSuffix(m.File, ".bin") {
			t.Fatalf("%s 文件名不合规：%s", m.ID, m.File)
		}
		for i := 0; i < len(m.File); i++ {
			if m.File[i] > 0x7F {
				t.Fatalf("%s 文件名含非 ASCII 字符", m.ID)
			}
		}
	}
	if _, ok := FindModel(DefaultModelID); !ok {
		t.Fatalf("默认模型 %s 不在清单里", DefaultModelID)
	}
}

// TestSourceListOrder 自定义源要排在最前，且不能与默认源重复。
func TestSourceListOrder(t *testing.T) {
	got := sourceList("https://my.mirror.example.com")
	if len(got) != 1+len(defaultSources) {
		t.Fatalf("源数量不对：%v", got)
	}
	if got[0] != "https://my.mirror.example.com/" {
		t.Fatalf("自定义源没排在最前：%v", got)
	}
	// 传一个与镜像相同的地址时不应重复。
	dup := sourceList(defaultSources[0])
	if len(dup) != len(defaultSources) {
		t.Fatalf("相同源被重复计入：%v", dup)
	}
}

// TestHostOf 下载源显示要干净。
func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/": "hf-mirror.com",
		"http://127.0.0.1:8080/x.bin":                               "127.0.0.1:8080",
		"nonsense":                                                  "nonsense",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Fatalf("hostOf(%q) = %q，应为 %q", in, got, want)
		}
	}
}

// TestPartialReported 有 .part 时要能报出来，界面才知道可以续传。
func TestPartialReported(t *testing.T) {
	m := withFakeModel(t, []byte(strings.Repeat("x", 1000)))
	mgr, dir := newTestMgr(t)
	_ = os.MkdirAll(filepath.Join(dir, "models"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "models", m.File+".part"), make([]byte, 400), 0o644)

	st := mgr.Snap(0, "", false)
	if len(st.Models) != 1 {
		t.Fatalf("模型数量不对：%d", len(st.Models))
	}
	if st.Models[0].Partial != 400 {
		t.Fatalf("partial 应为 400，实际 %d", st.Models[0].Partial)
	}
	if st.Models[0].Installed {
		t.Fatal("只有 .part 不算已安装")
	}
}

func waitDone(t *testing.T, mgr *Manager, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		p := mgr.Current()
		if p.State == "done" {
			return true
		}
		if p.State == "error" {
			t.Logf("下载失败：%s", p.Error)
			return false
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

var _ = context.Background
