package dsh

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestVersionAtLeast 盯住 Node 版本判断：22.19 是 DSH 的硬门槛，
// 判错一个位就会变成「装了但静默退出」。
func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		got, want string
		ok        bool
	}{
		{"v24.22.2", "22.19.0", true},
		{"v22.19.0", "22.19.0", true},
		{"v22.18.9", "22.19.0", false},
		{"v22.13.1", "22.19.0", false},
		{"v20.11.0", "22.19.0", false},
		{"v23.0.0", "22.19.0", true},
		{"24.1.0", "22.19.0", true},
		{"v24.22.2-nightly", "22.19.0", true},
		{"", "22.19.0", false},
		{"garbage", "22.19.0", false},
	}
	for _, c := range cases {
		if got := VersionAtLeast(c.got, c.want); got != c.ok {
			t.Errorf("VersionAtLeast(%q,%q)=%v, 期望 %v", c.got, c.want, got, c.ok)
		}
	}
}

// TestTokenParse 从 DSH 的启动行里取令牌。
func TestTokenParse(t *testing.T) {
	line := "dsh web: http://127.0.0.1:3099/?token=UT9ggLeFOrFegJ2qsSyA-AaG2t1R_EllCLUJ46H3_j4"
	m := tokenRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatal("没有解析出令牌")
	}
	if len(m[1]) < 16 {
		t.Fatalf("令牌太短：%q", m[1])
	}
	// 不能把普通日志误当成令牌。
	if tokenRe.FindStringSubmatch("dsh web: http://127.0.0.1:3099/") != nil {
		t.Fatal("没有令牌时不该匹配")
	}
}

// TestPluginsParse 读 profile 里已安装的插件。
func TestPluginsParse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "home", "profiles", "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
  "name": "dsh-profile-web",
  "private": true,
  "dependencies": {
    "dsh-help-me-to-ask": "^1.0.5",
    "@anonyjcy/dsh-j-space": "1.2.5"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(root, nil)
	got := r.Plugins()
	if len(got) != 2 {
		t.Fatalf("解析出 %d 个插件：%v", len(got), got)
	}
	if got[0] != "@anonyjcy/dsh-j-space" || got[1] != "dsh-help-me-to-ask" {
		t.Fatalf("插件列表不对：%v", got)
	}
}

// TestStatusNoRuntime 没装运行时时状态要说得清楚，而不是一片空白。
func TestStatusNoRuntime(t *testing.T) {
	r := New(t.TempDir(), nil)
	st := r.Status()
	if st.Installed {
		t.Fatal("空目录不该判定为已安装")
	}
	if st.Running {
		t.Fatal("空目录不该判定为运行中")
	}
	if st.Phase != PhaseIdle && st.Phase != PhaseError {
		t.Fatalf("初始阶段不对：%s", st.Phase)
	}
	if st.MinNode != MinNode {
		t.Fatalf("MinNode 没透出：%q", st.MinNode)
	}
}

// TestStatusNodeTooOld 明确的老版本 Node 必须被拒绝并给出原因。
func TestStatusNodeTooOld(t *testing.T) {
	if !VersionAtLeast("v22.13.1", MinNode) {
		// 这个用例只是把「22.13 不满足」这条事实钉在测试里。
		return
	}
	t.Fatal("22.13 不该满足 DSH 的要求")
}

// TestUnzipRejectsZipSlip 解压要挡住 ../ 越界条目。
func TestUnzipRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("pkg/../../evil.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("pwned")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out := filepath.Join(dir, "out")
	if err := unzipStrip(zipPath, out, "pkg/"); err != nil {
		t.Fatalf("解压不该报错：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("zip slip 没有被挡住")
	}
}

// TestUnzipStripsPrefix 正常条目要落到目标目录、并去掉顶层目录。
func TestUnzipStripsPrefix(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "ok.zip")
	f, _ := os.Create(zipPath)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("node-v24.22.2-win-x64/node.exe")
	_, _ = w.Write([]byte("MZ"))
	_ = zw.Close()
	_ = f.Close()

	out := filepath.Join(dir, "node")
	if err := unzipStrip(zipPath, out, "node-v24.22.2-win-x64/"); err != nil {
		t.Fatalf("解压失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "node.exe")); err != nil {
		t.Fatalf("node.exe 没解出来：%v", err)
	}
}

// TestPluginNameRejected 插件包名里不能夹带 shell 元字符。
func TestPluginNameRejected(t *testing.T) {
	r := New(t.TempDir(), nil)
	// 先把 profile 造出来，让前置检查通过，这样才能测到包名校验。
	dir := filepath.Join(r.root, "runtime", "node_modules", "@deepseek-ai", "dsh", "lib")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin.js"), []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.root, "runtime", "node_modules", "@deepseek-ai", "dsh-app-boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !r.Installed() {
		t.Fatal("构造的运行时没有被识别")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, bad := range []string{"", "a b", "x;rm -rf /", "$(id)", "a|b"} {
		if err := r.AddPlugin(ctx, bad, false); err == nil || err.Error() != "插件包名不合法" {
			t.Errorf("应拒绝 %q，实际 %v", bad, err)
		}
	}
}

// TestWriteLicense 装完必须留下许可原文：MIT 要求分发时保留声明。
func TestWriteLicense(t *testing.T) {
	root := t.TempDir()
	r := New(root, nil)
	r.writeLicense()

	lic, err := os.ReadFile(filepath.Join(root, "runtime", "LICENSE-DeepSeek-Harness.txt"))
	if err != nil {
		t.Fatalf("没有写出许可文件：%v", err)
	}
	if !strings.Contains(string(lic), "MIT License") || !strings.Contains(string(lic), "DeepSeek") {
		t.Fatalf("许可内容不对：%s", string(lic[:min(80, len(lic))]))
	}
	notice, err := os.ReadFile(filepath.Join(root, "runtime", "README-ClassNote.txt"))
	if err != nil {
		t.Fatalf("没有写出来源说明：%v", err)
	}
	if !strings.Contains(string(notice), "deepseek-harness") {
		t.Fatal("来源说明里没有仓库地址")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
