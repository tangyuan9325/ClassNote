// Package update 从 GitHub Releases 检查新版本。
package update

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"classnote/internal/app"
)

// Info 是一次检查结果。
type Info struct {
	Checked     bool   `json:"checked"`
	HasUpdate   bool   `json:"hasUpdate"`
	Current     string `json:"current"`
	Latest      string `json:"latest,omitempty"`
	URL         string `json:"url,omitempty"`
	Notes       string `json:"notes,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Error       string `json:"error,omitempty"`
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
	out.HasUpdate = newer(latest, cur)
	return out
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
