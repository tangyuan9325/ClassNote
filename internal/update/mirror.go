package update

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"classnote/internal/appinfo"
	"classnote/internal/logx"
)

// Node 是一个下载加速节点（GitHub 反代）。
//
// 为什么需要它：GitHub 的 release 下载在国内教室里经常是 20-60 KB/s，
// 一个 80 MB 的更新包要下半小时；节点则常常能跑到几 MB/s。
// 但节点是会挂的——今天能用的域名下个月就打不开了，所以节点表
// 只当候选，真正用哪个由运行时测速决定，而不是写死顺序。
type Node struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"` // 空串表示直连 github.com
	Note   string `json:"note"`
	Custom bool   `json:"custom,omitempty"`

	// 以下字段由测速填充
	Tested  bool   `json:"tested"`
	OK      bool   `json:"ok"`
	Latency int64  `json:"latencyMs"` // 首字节耗时
	Speed   int64  `json:"speed"`     // 字节/秒
	Size    int64  `json:"size,omitempty"`
	Error   string `json:"error,omitempty"`
}

// builtinNodes 是内置候选节点。
//
// 顺序不代表优先级（测速会重排），只影响「一个都测不通时」的回落顺序：
// 直连排第一，是为了在能直连的网络上不做任何多余的事。
func builtinNodes() []Node {
	return []Node{
		{ID: "direct", Name: "直连 GitHub", Prefix: "", Note: "官方地址，海外网络最快；国内常被限速"},
		{ID: "ghfast", Name: "ghfast.top", Prefix: "https://ghfast.top/", Note: "FastGit 系，国内多数地区可用"},
		{ID: "ghproxy-com", Name: "gh-proxy.com", Prefix: "https://gh-proxy.com/", Note: "老牌反代，支持 API 与 release"},
		{ID: "ghproxy-imciel", Name: "ghproxy.imciel.com", Prefix: "https://ghproxy.imciel.com/", Note: "带宽较大，晚高峰偶有抖动"},
		{ID: "ghproxy-net", Name: "ghproxy.net", Prefix: "https://ghproxy.net/", Note: "Cloudflare 线路"},
		{ID: "ghproxy-cc", Name: "ghproxy.cc", Prefix: "https://ghproxy.cc/", Note: "备用反代"},
		{ID: "ghproxy-cfd", Name: "ghproxy.cfd", Prefix: "https://ghproxy.cfd/", Note: "备用反代，不支持断点续传"},
		{ID: "llkk", Name: "gh.llkk.cc", Prefix: "https://gh.llkk.cc/", Note: "备用反代"},
		{ID: "moeyy", Name: "github.moeyy.xyz", Prefix: "https://github.moeyy.xyz/", Note: "备用反代"},
		{ID: "gitmirror", Name: "hub.gitmirror.com", Prefix: "https://hub.gitmirror.com/", Note: "GitMirror 线路"},
		{ID: "xxooo", Name: "gh.xxooo.cf", Prefix: "https://gh.xxooo.cf/", Note: "备用反代"},
	}
}

// Candidates 返回内置节点 + 用户自定义节点（自定义前缀也参与测速）。
func Candidates(custom []string) []Node {
	out := builtinNodes()
	for i, p := range custom {
		p = strings.TrimSpace(p)
		if !validPrefix(p) {
			continue
		}
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}
		out = append(out, Node{
			ID:     fmt.Sprintf("custom-%d", i+1),
			Name:   hostOf(p),
			Prefix: p,
			Note:   "自定义节点",
			Custom: true,
		})
	}
	return out
}

// validPrefix 校验自定义加速前缀。
//
// 只放行 https：这个地址会被拼上真实下载链后带着文件内容返回，
// 允许 http 等于把自己的更新包交给明文链路，也给了内网地址探测的机会。
func validPrefix(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != "" && u.RawQuery == ""
}

// Wrap 把原始 GitHub 地址包装成走该节点的地址。
func (n Node) Wrap(raw string) string {
	if n.Prefix == "" {
		return raw
	}
	return n.Prefix + raw
}

// Host 返回节点展示用的主机名。
func (n Node) Host() string {
	if n.Prefix == "" {
		return "github.com"
	}
	return hostOf(n.Prefix)
}

func hostOf(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

// ---- 测速 ----

const (
	probeWindow  = 2500 * time.Millisecond // 单节点采样窗口
	probeMaxByte = 1 << 20                 // 最多读 1 MB，够算出速度又不浪费流量
	probeTimeout = 10 * time.Second        // 单节点总超时
	probeWorkers = 6                       // 并发数：太多会互相抢带宽，测出来全是假的
)

// probeClient 专门用于测速：短超时、不走系统缓存。
var probeClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 8 * time.Second,
		DisableCompression:    true,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
	},
}

// ProbeReq 是一次测速请求。
type ProbeReq struct {
	URL        string   // 真实下载地址；为空时只返回未测速的节点表
	Custom     []string // 用户自定义前缀
	DirectOnly bool     // 私有仓库带 Token 时只能直连：反代拿不到鉴权头
	Force      bool     // 忽略缓存
}

// TestNodes 并发测速并返回按可用性排序的节点表。
func TestNodes(ctx context.Context, req ProbeReq) []Node {
	if ctx == nil {
		// 调用方传 nil 时不要直接 panic：这个函数在后台 goroutine 里跑，
		// 崩的是整个进程，而不是某个请求。
		ctx = context.Background()
	}
	nodes := Candidates(req.Custom)
	if req.DirectOnly {
		nodes = onlyDirect(nodes)
	}
	if strings.TrimSpace(req.URL) == "" {
		return nodes
	}
	if !req.Force {
		if cached, ok := cachedNodes(req.URL, req.DirectOnly); ok {
			return cached
		}
	}

	sem := make(chan struct{}, probeWorkers)
	var wg sync.WaitGroup
	out := make([]Node, len(nodes))
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			out[i] = probeOne(c, nodes[i], req.URL)
		}(i)
	}
	wg.Wait()

	ranked := rankNodes(out)
	storeCache(req.URL, req.DirectOnly, ranked)
	if best, ok := firstOK(ranked); ok {
		logx.Infof("update", "测速完成，选用节点 %s（%.1f KB/s，首字 %d ms）",
			best.Name, float64(best.Speed)/1024, best.Latency)
	} else {
		logx.Warnf("update", "所有加速节点都测不通，仍会按候选顺序尝试")
	}
	return ranked
}

func onlyDirect(nodes []Node) []Node {
	for _, n := range nodes {
		if n.Prefix == "" {
			return []Node{n}
		}
	}
	return nil
}

func probeOne(ctx context.Context, n Node, raw string) Node {
	n.Tested = true
	target := n.Wrap(raw)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		n.Error = "地址不合法"
		return n
	}
	// 只取前 1 MB：节点支持 Range 时这是几百 KB 的探测，
	// 不支持时也只是断开连接，不会把整个更新包拖下来。
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", probeMaxByte-1))
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "ClassNote/"+appinfo.Version)

	start := time.Now()
	resp, err := probeClient.Do(req)
	if err != nil {
		n.Error = friendlyNetErr(err)
		return n
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		n.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return n
	}
	n.Latency = time.Since(start).Milliseconds()
	if total := totalFromRange(resp.Header.Get("Content-Range")); total > 0 {
		n.Size = total
	} else if resp.ContentLength > 0 {
		n.Size = resp.ContentLength
	}

	buf := make([]byte, 64<<10)
	deadline := time.Now().Add(probeWindow)
	var got int64
	for got < probeMaxByte && time.Now().Before(deadline) {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			got += int64(k)
		}
		if rerr != nil {
			break
		}
	}
	if got == 0 {
		n.Error = "连接成功但收不到数据"
		return n
	}
	// 本机环回或局域网节点可能几十毫秒就读完，此时也算速度，
	// 否则「很快」会被记成 0，排序时反而排到最慢。
	if el := time.Since(start).Seconds(); el > 0.001 {
		n.Speed = int64(float64(got) / el)
	}
	if n.Speed <= 0 {
		n.Speed = 1
	}
	n.OK = true
	return n
}

// totalFromRange 从 "bytes 0-1048575/80289949" 里取总大小。
func totalFromRange(v string) int64 {
	if v == "" {
		return 0
	}
	i := strings.LastIndexByte(v, '/')
	if i < 0 {
		return 0
	}
	var n int64
	for _, c := range strings.TrimSpace(v[i+1:]) {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// rankNodes 排序规则：能用的在前，速度快的在前，速度接近时延迟低的在前。
//
// 只看延迟会把「握手快、下载慢」的节点排前面；只看速度又会让首字节
// 慢三秒的节点拖住界面。所以先按速度排，速度差 15% 以内再比延迟。
func rankNodes(nodes []Node) []Node {
	out := append([]Node(nil), nodes...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.OK != b.OK {
			return a.OK
		}
		if !a.OK {
			return a.Name < b.Name
		}
		if b.Speed > 0 {
			diff := math.Abs(float64(a.Speed-b.Speed)) / float64(b.Speed)
			if diff <= 0.15 && a.Latency != b.Latency {
				return a.Latency < b.Latency
			}
		}
		return a.Speed > b.Speed
	})
	return out
}

func firstOK(nodes []Node) (Node, bool) {
	for _, n := range nodes {
		if n.OK {
			return n, true
		}
	}
	return Node{}, false
}

// ---- 缓存 ----

const cacheTTL = 10 * time.Minute

type cacheEntry struct {
	at    time.Time
	nodes []Node
	key   string
}

var (
	cacheMu   sync.Mutex
	nodeCache = map[string]cacheEntry{}
)

func cacheKey(raw string, directOnly bool) string {
	if directOnly {
		return "direct|" + raw
	}
	return "all|" + raw
}

func cachedNodes(raw string, directOnly bool) ([]Node, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	e, ok := nodeCache[cacheKey(raw, directOnly)]
	if !ok || time.Since(e.at) > cacheTTL {
		return nil, false
	}
	return append([]Node(nil), e.nodes...), true
}

func storeCache(raw string, directOnly bool, nodes []Node) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	k := cacheKey(raw, directOnly)
	// 只留最近 8 个地址：缓存是给「同一个包反复重试」用的，不需要长记忆。
	if len(nodeCache) > 8 {
		nodeCache = map[string]cacheEntry{}
	}
	nodeCache[k] = cacheEntry{at: time.Now(), nodes: append([]Node(nil), nodes...), key: k}
}

// Endpoints 把排序后的节点转换成下载器要用的地址列表。
func Endpoints(raw string, nodes []Node) []Endpoint {
	out := make([]Endpoint, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, Endpoint{ID: n.ID, Name: n.Name, URL: n.Wrap(raw)})
	}
	return out
}

func friendlyNetErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "no such host"):
		return "域名解析失败"
	case strings.Contains(s, "connection refused"):
		return "连接被拒绝"
	case strings.Contains(s, "context deadline exceeded"), strings.Contains(s, "timeout"):
		return "连接超时"
	case strings.Contains(s, "tls:"):
		return "TLS 握手失败"
	case strings.Contains(s, "EOF"):
		return "连接被对端关闭"
	}
	if ue, ok := err.(*url.Error); ok && ue.Err != nil {
		return ue.Err.Error()
	}
	return s
}

// Prefer 把指定 ID 的节点提到最前面，其余保持原有顺序。
//
// 用户手动锁定节点、或上次测速选中的节点，都靠它优先复用：
// 每次下载都重新测速要多等十秒，而网络不会在十分钟内变两次。
func Prefer(nodes []Node, id string) []Node {
	if id == "" {
		return nodes
	}
	out := make([]Node, 0, len(nodes))
	var hit []Node
	for _, n := range nodes {
		if n.ID == id {
			hit = append(hit, n)
			continue
		}
		out = append(out, n)
	}
	return append(hit, out...)
}

// Cached 返回某个地址的测速缓存；没有缓存时返回未测速的候选表。
func Cached(raw string, directOnly bool, custom []string) ([]Node, bool) {
	if raw != "" {
		if n, ok := cachedNodes(raw, directOnly); ok {
			return n, true
		}
	}
	return Candidates(custom), false
}
