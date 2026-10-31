// Package update 从 GitHub Releases 检查新版本，支持直接下载安装。
package update

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequest("GET", assetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ClassNote")
	if token != "" {
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
		name = filepath.Base(strings.Split(assetURL, "?")[0])
	}
	dst := filepath.Join(destDir, name)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
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
		_, _ = io.Copy(out, rc)
		out.Close()
		rc.Close()
	}
	return nil
}

// newer 判断 a 是否比 b 新（语义化版本 x.y.z）。
func newer(a, b string) bool {
	pa, pb := splitVer(a), splitVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func splitVer(v string) [3]int {
	var out [3]int
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}
