// Package update 从 GitHub Releases 检查新版本，支持直接下载安装。
package update

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"classnote/internal/app"
)

// Asset 是 Release 里的一个附件。
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"` // browser_download_url
	Size int64  `json:"size,omitempty"`
}

// Info 是一次检查结果。
type Info struct {
	Checked     bool    `json:"checked"`
	HasUpdate   bool    `json:"hasUpdate"`
	Current     string  `json:"current"`
	Latest      string  `json:"latest,omitempty"`
	URL         string  `json:"url,omitempty"`
	Notes       string  `json:"notes,omitempty"`
	PublishedAt string  `json:"publishedAt,omitempty"`
	Assets      []Asset `json:"assets,omitempty"`
	Error       string  `json:"error,omitempty"`
}

var cli = &http.Client{Timeout: 20 * time.Second}

// Check 查询 GitHub 上的最新 Release。
//
// repo 形如 "owner/name"；token 可选（私有仓库需要）。
func Check(repo, token string) Info {
	cur := strings.TrimPrefix(app.Version, "v")
	out := Info{Checked: true, Current: cur}
	if repo == "" {
		out.Error = "未配置仓库地址"
		return out
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ClassNote/"+cur)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := cli.Do(req)
	if err != nil {
		out.Error = "连接 GitHub 失败：" + err.Error()
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		out.Error = "仓库还没有发布任何版本（去 GitHub 创建 Release 后即可检测更新）"
		return out
	}
	if resp.StatusCode >= 400 {
		out.Error = fmt.Sprintf("GitHub 返回 %d", resp.StatusCode)
		return out
	}
	var rel struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		Name        string    `json:"name"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		Assets      []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		out.Error = "解析 Release 失败"
		return out
	}
	if rel.Draft || rel.Prerelease {
		out.Error = "最新版本是草稿/预发布，跳过"
		return out
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	out.Latest = latest
	out.URL = rel.HTMLURL
	out.Notes = rel.Body
	if !rel.PublishedAt.IsZero() {
		out.PublishedAt = rel.PublishedAt.Format("2006-01-02 15:04")
	}
	for _, a := range rel.Assets {
		out.Assets = append(out.Assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	out.HasUpdate = newer(latest, cur)
	return out
}

// Download 下载指定附件到 destDir，返回保存路径。
func Download(assetURL, destDir, name, token string) (string, error) {
	if assetURL == "" {
		return "", fmt.Errorf("附件地址为空")
	}
	u, err := url.Parse(assetURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("附件地址不合法")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequest("GET", assetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ClassNote")
	// Token 只发给 GitHub 自己的域：这个接口的 url 参数来自请求体，
	// 无条件带 Authorization 等于把凭据交给任意主机。
	if token != "" && trustedHost(u.Hostname()) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// 大文件下载不设总超时
	dl := &http.Client{Timeout: 0}
	resp, err := dl.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	if name == "" {
		name = filepath.Base(u.Path)
	}
	// 只取最后一段并去掉 ..，否则 name 传 "../../x.exe" 就能写到程序目录之外。
	name = filepath.Base(filepath.Clean("/" + name))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		return "", fmt.Errorf("附件文件名不合法")
	}
	dst := filepath.Join(destDir, name)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// 限流：更新包最大 200MB，避免对端返回一个超大响应把磁盘写满。
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 200<<20)); err != nil {
		return "", fmt.Errorf("写入失败: %w", err)
	}
	return dst, nil
}

// Apply 解压 zip 到安装目录旁的 update-staging，并启动替换脚本完成升级。
//
// 流程：解压到 <root>/update-staging → 写 update.cmd（等本进程退出后覆盖文件并重启）
// → 隐藏启动脚本 → 调用方随后退出程序即可完成升级。
func Apply(zipPath, root string) error {
	staging := filepath.Join(root, "update-staging")
	_ = os.RemoveAll(staging)
	if err := unzip(zipPath, staging); err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}
	// 解压出的文件可能套了一层版本目录，取到实际内容层
	content := staging
	entries, _ := os.ReadDir(staging)
	if len(entries) == 1 && entries[0].IsDir() {
		content = filepath.Join(staging, entries[0].Name())
	}
	// 校验包里确实有 ClassNote.exe，且是 Windows PE。
	// 没有这一步，任何一个 zip 都会被 xcopy 覆盖进安装目录——
	// 更新包来源只有 GitHub Releases，但这仍是最便宜的一道闸。
	if err := verifyPayload(content); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	exe, _ := os.Executable()
	script := fmt.Sprintf(`@echo off
setlocal
:wait
tasklist /fi "pid eq %d" 2>nul | find "%d" >nul
if not errorlevel 1 (
  timeout /t 1 /nobreak >nul
  goto wait
)
xcopy /s /e /y "%s\*" "%s\" >nul
start "" "%s"
rd /s /q "%s"
del "%%~f0"
`, os.Getpid(), os.Getpid(), content, root, exe, staging)
	cmdPath := filepath.Join(root, "update.cmd")
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("cmd", "/c", cmdPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动升级脚本失败: %w", err)
	}
	return nil
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	var total int64
	var lastErr error
	for _, f := range r.File {
		target := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) {
			continue // 防 zip slip
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			continue
		}
		// 单文件限 200MB，累计限 600MB：解压炸弹会在这一步停下。
		written, cerr := io.Copy(out, io.LimitReader(rc, 200<<20))
		out.Close()
		rc.Close()
		total += written
		if cerr != nil || total > 600<<20 {
			_ = os.Remove(target)
			if cerr != nil {
				lastErr = cerr
			} else {
				lastErr = fmt.Errorf("更新包解压后超过 600MB，已中止")
				break
			}
		}
	}
	return lastErr
}

// newer 判断 a 是否比 b 新（语义化版本 x.y.z）。
// newer 判断 a 是否比 b 新。
//
// 按段比较，段数不限：1.2.6 之前写死成 3 段，于是 1.2.6.1 和 1.2.6 被判成
// 「同一版本」，发出去的补丁版用户永远收不到更新提示。
func newer(a, b string) bool {
	pa, pb := splitVer(a), splitVer(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var va, vb int
		if i < len(pa) {
			va = pa[i]
		}
		if i < len(pb) {
			vb = pb[i]
		}
		if va != vb {
			return va > vb
		}
	}
	return false
}

// splitVer 把 "v1.2.6.1-beta" 拆成 [1 2 6 1]。
// 遇到非数字段就停下，避免把 "beta" 当成 0 段。
func splitVer(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// trustedHost 判断是否 GitHub 自有域，只有这些主机才允许收到 Token。
func trustedHost(host string) bool {
	h := strings.ToLower(host)
	if h == "github.com" || h == "api.github.com" || h == "codeload.github.com" || h == "objects.githubusercontent.com" {
		return true
	}
	return strings.HasSuffix(h, ".githubusercontent.com")
}

// verifyPayload 确认解压目录里有一个像样的 ClassNote.exe。
func verifyPayload(dir string) error {
	path := filepath.Join(dir, "ClassNote.exe")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("更新包里没有 ClassNote.exe，已取消升级")
	}
	defer f.Close()
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil || head[0] != 'M' || head[1] != 'Z' {
		return fmt.Errorf("更新包里的 ClassNote.exe 不是有效的 Windows 程序，已取消升级")
	}
	return nil
}
