// Package update 从 GitHub Releases 检查新版本，支持直接下载安装。
package update

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"classnote/internal/appinfo"
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
	// Source 说明这次是通过哪条路查到的，排查「为什么没提示更新」时
	// 光看 Error 不够——有时候是查到了，只是走的是兜底通道。
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

var cli = &http.Client{Timeout: 20 * time.Second}

// apiBase 是 GitHub API 的官方地址。
const apiBase = "https://api.github.com"

// relayAPI 是唯一实测支持转发 API 的反代（很多反代只放行 release 附件）。
const relayAPI = "https://gh-proxy.com/"

// Check 查询 GitHub 上的最新 Release。
//
// repo 形如 "owner/name"；token 可选（私有仓库需要）。
//
// 三级回落：直连 api.github.com → 反代转发 API → releases.atom。
// 学校网络里 api.github.com 被 DNS 污染或 TLS 阻断是常事，只走第一级
// 的话，界面会长期停在「检查失败」，而实际上 release 页面还能打开。
func Check(repo, token string) Info {
	cur := strings.TrimPrefix(appinfo.Version, "v")
	out := Info{Checked: true, Current: cur}
	if repo == "" {
		out.Error = "未配置仓库地址"
		return out
	}
	repo = strings.Trim(strings.TrimSpace(repo), "/")

	var errs []string
	info, err := checkAPI(apiBase, repo, token, "api.github.com")
	if err == nil {
		info.Current = cur
		info.HasUpdate = newer(info.Latest, cur)
		return info
	}
	errs = append(errs, "直连 API："+err.Error())

	// 带 Token 时不走反代：凭据不该交给第三方，而且反代也拿不到鉴权头。
	if token == "" {
		info, err = checkAPI(relayAPI+apiBase, repo, "", "api via gh-proxy.com")
		if err == nil {
			info.Current = cur
			info.HasUpdate = newer(info.Latest, cur)
			return info
		}
		errs = append(errs, "反代 API："+err.Error())

		info, err = checkAtom(repo)
		if err == nil {
			info.Current = cur
			info.HasUpdate = newer(info.Latest, cur)
			return info
		}
		errs = append(errs, "atom："+err.Error())
	}

	out.Error = "检查更新失败（" + strings.Join(errs, "；") + "）"
	return out
}

// checkAPI 走 /releases/latest 接口。base 可以是官方地址，也可以是反代前缀。
func checkAPI(base, repo, token, source string) (Info, error) {
	var out Info
	target := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(base, "/"), repo)
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ClassNote/"+strings.TrimPrefix(appinfo.Version, "v"))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return out, fmt.Errorf("仓库还没有发布任何版本（去 GitHub 创建 Release 后即可检测更新）")
	}
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
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
		return out, fmt.Errorf("解析 Release 失败")
	}
	if rel.Draft || rel.Prerelease {
		return out, fmt.Errorf("最新版本是草稿/预发布，跳过")
	}
	out.Checked = true
	out.Source = source
	out.Latest = strings.TrimPrefix(rel.TagName, "v")
	out.URL = rel.HTMLURL
	out.Notes = strings.TrimSpace(rel.Body)
	if !rel.PublishedAt.IsZero() {
		out.PublishedAt = rel.PublishedAt.Format("2006-01-02 15:04")
	}
	for _, a := range rel.Assets {
		out.Assets = append(out.Assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return out, nil
}

// checkAtom 抓 releases.atom 兜底。
//
// 这是一条「页面级」通道：能拿到版本号、发布时间与更新说明，
// 但拿不到附件列表。所以后面还要按发布包命名约定去探一次——
// 探不到也不算失败，界面至少能提示「有新版本」，让用户去 Releases 页手动下。
func checkAtom(repo string) (Info, error) {
	var out Info
	raw := fmt.Sprintf("https://github.com/%s/releases.atom", repo)
	bases := []string{"", "https://ghfast.top/", relayAPI}
	var lastErr error
	var body []byte
	for _, b := range bases {
		req, err := http.NewRequest("GET", b+raw, nil)
		if err != nil {
			lastErr = err
			continue
		}
		// Atom 是页面而不是 API：不带 Accept: application/json。
		req.Header.Set("User-Agent", "ClassNote/"+strings.TrimPrefix(appinfo.Version, "v"))
		resp, err := cli.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			resp.Body.Close()
			continue
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err == nil && len(body) > 0 {
			lastErr = nil
			break
		}
		if err != nil {
			lastErr = err
		}
	}
	if len(body) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("没有返回内容")
		}
		return out, lastErr
	}

	tag, href, published, notes := newestEntry(string(body))
	if tag == "" {
		return out, fmt.Errorf("原子订阅里没有版本条目")
	}
	out.Checked = true
	out.Source = "releases.atom（经加速节点）"
	out.Latest = strings.TrimPrefix(tag, "v")
	if out.Latest == "" {
		out.Latest = versionFromHref(href)
	}
	out.URL = href
	out.Notes = stripHTML(notes)
	if published != "" {
		if t, err := time.Parse(time.RFC3339, published); err == nil {
			out.PublishedAt = t.Format("2006-01-02 15:04")
		}
	}
	// 附件名有固定约定，探一下能省掉用户手动找包的一步。
	out.Assets = probeAssets(repo, out.Latest)
	return out, nil
}

var (
	entryRe     = regexp.MustCompile(`(?s)<entry>(.*?)</entry>`)
	entryTagRe  = regexp.MustCompile(`(?s)<link[^>]*href="([^"]*)"`)
	publishedRe = regexp.MustCompile(`(?s)<updated>([^<]*)</updated>`)
	titleRe     = regexp.MustCompile(`(?s)<title>(.*?)</title>`)
	contentRe   = regexp.MustCompile(`(?s)<content[^>]*>(.*?)</content>`)
	tagLinkRe   = regexp.MustCompile(`/releases/tag/([^/"]+)$`)
)

// newestEntry 从 atom XML 里取出最新一条的标签、链接、时间与说明。
//
// 不用 encoding/xml 是因为这份订阅的结构在 GitHub 侧改过几回，
// 用正则只依赖「entry 里有 link/href、updated、content」这几条稳定事实。
func newestEntry(xml string) (tag, href, published, notes string) {
	entries := entryRe.FindAllStringSubmatch(xml, -1)
	if len(entries) == 0 {
		return "", "", "", ""
	}
	first := entries[0][1]
	if m := titleRe.FindStringSubmatch(first); len(m) > 1 {
		tag = strings.TrimSpace(stripHTML(m[1]))
	}
	if m := entryTagRe.FindStringSubmatch(first); len(m) > 1 {
		href = strings.TrimSpace(m[1])
	}
	if m := publishedRe.FindStringSubmatch(first); len(m) > 1 {
		published = strings.TrimSpace(m[1])
	}
	if m := contentRe.FindStringSubmatch(first); len(m) > 1 {
		notes = m[1]
	}
	return tag, href, published, notes
}

// versionFromHref 从 releases/tag/v1.8.1 这类链接里取版本号。
func versionFromHref(href string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	m := tagLinkRe.FindStringSubmatch(u.Path)
	if len(m) < 2 {
		return ""
	}
	v := strings.TrimPrefix(m[1], "v")
	if _, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0]); err != nil {
		return ""
	}
	return v
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

// stripHTML 把 HTML 摘要变成纯文本：去标签、还原常见实体、压掉空行。
func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n")
	s = strings.ReplaceAll(s, "</li>", "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&amp;", "&")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

// probeAssets 按发布包命名约定探测附件地址。
//
// 只探 HEAD/Range 的第一个字节，不真下：探到了界面就能直接点「下载」，
// 探不到也只是少一个按钮，不影响「有新版本」这个结论。
func probeAssets(repo, version string) []Asset {
	if version == "" {
		return nil
	}
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	names := []string{
		fmt.Sprintf("ClassNote-%s-windows-amd64.zip", version),
		fmt.Sprintf("ClassNote-%s.zip", version),
	}
	var out []Asset
	for _, n := range names {
		direct := fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", repo, version, n)
		if size, ok := probeAsset(direct); ok {
			out = append(out, Asset{Name: n, URL: direct, Size: size})
			break
		}
		// 直连探不通时借一个节点探：能探到就仍记录官方地址，
		// 下载时再按测速结果套节点，免得把某个反代写进数据里。
		if size, ok := probeAsset("https://ghfast.top/" + direct); ok {
			out = append(out, Asset{Name: n, URL: direct, Size: size})
			break
		}
	}
	return out
}

// probeAsset 用 Range 请求确认地址可用，并顺带取回体积。
func probeAsset(raw string) (int64, bool) {
	req, err := http.NewRequest("GET", raw, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", "ClassNote/"+strings.TrimPrefix(appinfo.Version, "v"))
	resp, err := cli.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, false
	}
	if n := totalFromRange(resp.Header.Get("Content-Range")); n > 0 {
		return n, true
	}
	if resp.ContentLength > 0 {
		return resp.ContentLength, true
	}
	return 0, true
}

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
