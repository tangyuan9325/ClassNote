package plugin

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 市场渠道。
const (
	ChannelClassNote = "classnote"
	ChannelDSH       = "dsh"
)

//go:embed skills.json
var bundledSkills []byte

//go:embed dsh-plugins.json.gz
var bundledDSH []byte

// MarketItem 是市场里的一条。
type MarketItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Author      string   `json:"author,omitempty"`
	Description string   `json:"description,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	URL         string   `json:"url,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	License     string   `json:"license,omitempty"`
	Category    string   `json:"category,omitempty"`
	Channel     string   `json:"channel,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Package     string   `json:"package,omitempty"`
	Downloads   int      `json:"downloads,omitempty"`
	Stars       int      `json:"stars,omitempty"`
	Installed   bool     `json:"installed,omitempty"`
}

// MarketChannel 描述一个可选渠道。
type MarketChannel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Note   string `json:"note"`
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// MarketResult 是一次市场查询的结果。
type MarketResult struct {
	Channel    string            `json:"channel"`
	Channels   []MarketChannel   `json:"channels"`
	Categories map[string]string `json:"categories,omitempty"`
	Items      []MarketItem      `json:"items"`
	Total      int               `json:"total"`
	Source     string            `json:"source"`
	Stale      bool              `json:"stale,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// DSH 插件索引：官方格式 1（dsh-plugin-catalog），这里用精简快照。
const (
	dshIndexDefault = "https://raw.githubusercontent.com/tangyuan9325/ClassNote/main/dsh-plugins.json"
	dshIndexFormat  = "classnote.dsh-plugins/1"
	dshIndexLimit   = 6 << 20
)

type dshIndex struct {
	Schema     string            `json:"schema"`
	Source     string            `json:"source"`
	Updated    string            `json:"updated"`
	Categories map[string]string `json:"categories"`
	Plugins    []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Desc     string `json:"desc"`
		URL      string `json:"url"`
		Category string `json:"category"`
		Version  string `json:"version"`
		Pkg      string `json:"pkg"`
	} `json:"plugins"`
}

var (
	dshOnce   sync.Once
	dshCache  dshIndex
	dshErr    error
	dshClient = &http.Client{Timeout: 12 * time.Second}
)

// loadDSH 解析内置快照。索引文件在编译期生成，运行时零网络依赖。
func loadDSH() (dshIndex, error) {
	dshOnce.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(bundledDSH))
		if err != nil {
			dshErr = err
			return
		}
		raw, err := io.ReadAll(io.LimitReader(zr, dshIndexLimit))
		if err != nil {
			dshErr = err
			return
		}
		var doc dshIndex
		if err := json.Unmarshal(raw, &doc); err != nil {
			dshErr = err
			return
		}
		dshCache = doc
	})
	return dshCache, dshErr
}

// LoadDSHIndex 读磁盘缓存或远端快照，失败时回退内置清单。
//
// 和 ClassNote 渠道一样，这里不做同步网络请求：首次用内置快照，
// 磁盘缓存在后台刷新。老师打开市场页不应该看到转圈。
func LoadDSHIndex(cachePath, remote string) (dshIndex, bool) {
	base, err := loadDSH()
	if err != nil {
		return dshIndex{}, true
	}
	if cachePath == "" {
		return base, true
	}
	dshCacheMu.Lock()
	doc, at, ok := dshDisk, dshDiskAt, dshDiskOK
	dshCacheMu.Unlock()
	if ok && time.Since(at) < 24*time.Hour {
		return doc, false
	}
	if remote != "" {
		go refreshDSHIndex(cachePath, remote)
	}
	if ok {
		return doc, true
	}
	return base, true
}

var (
	dshCacheMu sync.Mutex
	dshDisk    dshIndex
	dshDiskAt  time.Time
	dshDiskOK  bool
	dshBusy    bool
)

func refreshDSHIndex(cachePath, remote string) {
	dshCacheMu.Lock()
	if dshBusy {
		dshCacheMu.Unlock()
		return
	}
	dshBusy = true
	dshCacheMu.Unlock()
	defer func() {
		dshCacheMu.Lock()
		dshBusy = false
		dshCacheMu.Unlock()
	}()

	// 先看磁盘上有没有比内置更新的快照。
	if raw, err := os.ReadFile(cachePath); err == nil {
		var doc dshIndex
		if json.Unmarshal(raw, &doc) == nil && len(doc.Plugins) > 0 {
			fi, _ := os.Stat(cachePath)
			dshCacheMu.Lock()
			dshDisk, dshDiskAt, dshDiskOK = doc, fi.ModTime(), true
			dshCacheMu.Unlock()
			if time.Since(fi.ModTime()) < 24*time.Hour {
				return
			}
		}
	}

	req, err := http.NewRequest("GET", remote, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "ClassNote")
	resp, err := dshClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, dshIndexLimit))
	if err != nil || len(raw) < 1024 {
		return
	}
	var doc dshIndex
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Plugins) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		tmp := cachePath + ".tmp"
		if os.WriteFile(tmp, raw, 0o644) == nil {
			_ = os.Rename(tmp, cachePath)
		}
	}
	dshCacheMu.Lock()
	dshDisk, dshDiskAt, dshDiskOK = doc, time.Now(), true
	dshCacheMu.Unlock()
}

// safeIndexURL 校验市场清单地址。
//
// 这个地址来自查询串，如果放任不管，本机任意网页都能让服务去请求
// 内网地址（127.0.0.1:8080、192.168.x.x），等于把 ClassNote 当成跳板。
// 只允许 https，且不允许解析到回环/内网的主机名。
func safeIndexURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return false
		}
	}
	return true
}

// Market 查询某一渠道的插件；query 为空表示全部（按 limit 截断）。
func (r *Registry) Market(channel, query, category string, limit, dshLimit int, cachePath, remote string) MarketResult {
	if channel == "" {
		channel = ChannelClassNote
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if remote != "" && !safeIndexURL(remote) {
		remote = ""
	}
	installed := map[string]bool{}
	if r != nil {
		for _, p := range r.List() {
			installed[p.ID] = true
		}
	}
	res := MarketResult{Channel: channel}
	switch channel {
	case ChannelDSH:
		doc, stale := LoadDSHIndex(cachePath, remote)
		res.Categories = doc.Categories
		res.Source = doc.Source
		if res.Source == "" {
			res.Source = dshIndexDefault
		}
		res.Stale = stale
		items := make([]MarketItem, 0, len(doc.Plugins))
		q := strings.ToLower(strings.TrimSpace(query))
		for _, p := range doc.Plugins {
			if category != "" && p.Category != category {
				continue
			}
			if q != "" && !matchAny(q, p.Name, p.Desc, p.Category, p.Pkg) {
				continue
			}
			items = append(items, MarketItem{
				ID: p.ID, Name: p.Name, Description: p.Desc, URL: p.URL,
				Category: p.Category, Version: p.Version, Package: p.Pkg,
				Channel: ChannelDSH, Kind: KindExternal,
				Installed: installed[p.ID],
			})
		}
		res.Total = len(items)
		if dshLimit <= 0 {
			dshLimit = 2000
		}
		if len(items) > dshLimit {
			items = items[:dshLimit]
		}
		res.Items = items
	default:
		res.Channel = ChannelClassNote
		items, err := classNoteMarket(remote)
		if err != nil {
			res.Error = err.Error()
		}
		out := make([]MarketItem, 0, len(items))
		q := strings.ToLower(strings.TrimSpace(query))
		for _, it := range items {
			it.Channel = ChannelClassNote
			if it.Kind == "" {
				it.Kind = KindSkill
			}
			it.Installed = installed[it.ID]
			if q != "" && !matchAny(q, it.Name, it.Description, it.Author) {
				continue
			}
			out = append(out, it)
		}
		res.Total = len(out)
		if len(out) > limit {
			out = out[:limit]
		}
		res.Items = out
		res.Source = "skills.json"
	}
	res.Channels = r.channels(res.Total)
	return res
}

// channels 生成渠道列表（带当前条目数）。
func (r *Registry) channels(classNoteCount int) []MarketChannel {
	count := 0
	if doc, err := loadDSH(); err == nil {
		count = len(doc.Plugins)
	}
	return []MarketChannel{
		{ID: ChannelClassNote, Name: "ClassNote", Count: classNoteCount,
			Note:   "本机课堂助手用的提示词型插件，安装后直接生效",
			Source: "https://raw.githubusercontent.com/tangyuan9325/ClassNote/main/skills.json"},
		{ID: ChannelDSH, Name: "DeepSeek Harness", Count: count,
			Note:   "DSH 生态的 Node 插件索引，可在 ClassNote 里检索、登记、复制安装命令",
			Source: dshIndexDefault},
	}
}

// classNote 渠道的清单缓存。
//
// 以前每次搜索都同步请求 GitHub：老师每敲一个字就发一次请求，
// 网络不通时每次卡 12 秒，界面像死了一样。现在改成
// 「有缓存就用缓存，过期了在后台刷新」，界面永远不会等网络。
var (
	cnMu      sync.Mutex
	cnItems   []MarketItem
	cnAt      time.Time
	cnFetched bool
	cnBusy    bool
)

const cnTTL = 10 * time.Minute

func classNoteMarket(remote string) ([]MarketItem, error) {
	if remote == "" {
		remote = "https://raw.githubusercontent.com/tangyuan9325/ClassNote/main/skills.json"
	}
	cnMu.Lock()
	cached, fetched, at, busy := cnItems, cnFetched, cnAt, cnBusy
	cnMu.Unlock()

	fresh := fetched && time.Since(at) < cnTTL
	if fresh {
		return cached, nil
	}
	if !fetched && !busy {
		// 首次进入市场：先给内置清单，远端在后台拉。
		go refreshClassNote(remote)
		items, _ := parseSkillMarket(bundledSkills)
		return items, nil
	}
	if fetched {
		if !busy {
			go refreshClassNote(remote)
		}
		return cached, nil
	}
	items, _ := parseSkillMarket(bundledSkills)
	return items, nil
}

func refreshClassNote(remote string) {
	cnMu.Lock()
	if cnBusy {
		cnMu.Unlock()
		return
	}
	cnBusy = true
	cnMu.Unlock()
	defer func() {
		cnMu.Lock()
		cnBusy = false
		cnMu.Unlock()
	}()

	req, err := http.NewRequest("GET", remote, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "ClassNote")
	resp, err := dshClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return
	}
	items, err := parseSkillMarket(raw)
	if err != nil || len(items) == 0 {
		return
	}
	items = mergeBundledMeta(items)
	cnMu.Lock()
	cnItems, cnAt, cnFetched = items, time.Now(), true
	cnMu.Unlock()
}

func parseSkillMarket(raw []byte) ([]MarketItem, error) {
	var w struct {
		Skills []MarketItem `json:"skills"`
	}
	if err := json.Unmarshal(raw, &w); err == nil && len(w.Skills) > 0 {
		return w.Skills, nil
	}
	var arr []MarketItem
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}
	return nil, fmt.Errorf("市场清单格式不正确")
}

// mergeBundledMeta 用随包清单补全远端缺失的下载量/星标，
// 这样即使 GitHub 上的 skills.json 还是旧格式，界面也能显示完整信息。
func mergeBundledMeta(items []MarketItem) []MarketItem {
	local, err := parseSkillMarket(bundledSkills)
	if err != nil {
		return items
	}
	meta := make(map[string]MarketItem, len(local))
	for _, s := range local {
		meta[s.ID] = s
	}
	for i := range items {
		if m, ok := meta[items[i].ID]; ok {
			if items[i].Downloads == 0 {
				items[i].Downloads = m.Downloads
			}
			if items[i].Stars == 0 {
				items[i].Stars = m.Stars
			}
		}
	}
	return items
}

// InstallCommand 返回 DSH 插件在 DSH 里的安装命令（用于界面上复制）。
func InstallCommand(m MarketItem) string {
	pkg := m.Package
	if pkg == "" {
		pkg = m.Name
	}
	return "dsh plugin --profile web add " + pkg
}

// Categories 返回 DSH 索引的分类 id → 中文名。
func Categories() map[string]string {
	doc, err := loadDSH()
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(doc.Categories))
	for k, v := range doc.Categories {
		out[k] = v
	}
	return out
}

// CategoryList 按条目数排序返回分类。
func CategoryList() []MarketChannel {
	doc, err := loadDSH()
	if err != nil {
		return nil
	}
	count := map[string]int{}
	for _, p := range doc.Plugins {
		count[p.Category]++
	}
	out := make([]MarketChannel, 0, len(doc.Categories))
	for id, title := range doc.Categories {
		out = append(out, MarketChannel{ID: id, Name: title, Count: count[id]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func matchAny(q string, fields ...string) bool {
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// Clip 按字数截断，市场描述来自第三方，必须限长后再进前端。
func Clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
