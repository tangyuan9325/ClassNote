package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"classnote/internal/config"
	"classnote/internal/logx"
)

// WebClient 通过浏览器自动化使用网页版 AI（DeepSeek / 通义千问）。
type WebClient struct {
	subject string
	cfg     config.WebAIConfig
	mu      sync.Mutex
	br      *rod.Browser
	temp    bool
	dir     string
}

// SitePreset 是网页版站点的默认选择器。
type SitePreset struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Note      string            `json:"note"`
	Selectors map[string]string `json:"selectors"`
}

// WebSites 返回内置站点。
func WebSites() []SitePreset {
	return []SitePreset{
		{ID: "qwen", Name: "通义千问（网页版）", URL: "https://chat.qwen.ai/",
			Note: "支持上传图片，免费额度充足",
			Selectors: map[string]string{
				"input": "textarea.message-input-textarea, textarea",
				"send":  "button.send-button, button[aria-label=Send], .message-input-right-button-send, button[class*=send], button[type=submit]",
				"reply": ".qwen-chat-message-assistant .qwen-markdown, .qwen-markdown, [class*=markdown]",
				"file":  "input[type=file]",
			}},
		{ID: "deepseek", Name: "DeepSeek（网页版）", URL: "https://chat.deepseek.com/",
			Note: "需要登录，长文本理解能力强",
			Selectors: map[string]string{
				"input": "textarea",
				"send":  "div[role=button][class*=send], button[type=submit]",
				"reply": ".ds-markdown, [class*=markdown]",
				"file":  "input[type=file]",
			}},
		{ID: "custom", Name: "自定义网页", URL: "",
			Note:      "自行填写地址与选择器",
			Selectors: map[string]string{"input": "textarea", "reply": ".markdown-body, [class*=markdown]"}},
	}
}

// NewWebClient 创建网页版客户端。
func NewWebClient(cfg config.Config) (Client, error) {
	site := cfg.WebAI.Site
	if site == "" {
		site = "qwen"
	}
	if cfg.WebAI.URL == "" {
		for _, s := range WebSites() {
			if s.ID == site {
				cfg.WebAI.URL = s.URL
				if len(cfg.WebAI.Selectors) == 0 {
					cfg.WebAI.Selectors = s.Selectors
				}
			}
		}
	}
	if cfg.WebAI.URL == "" {
		return nil, fmt.Errorf("网页版 AI 未配置地址")
	}
	dir := cfg.WebAI.ProfileDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "classnote-webprofile")
	}
	return &WebClient{cfg: cfg.WebAI, dir: dir}, nil
}

// Describe 返回描述。
func (c *WebClient) Describe() string { return "网页版 · " + hostOf(c.cfg.URL) }

// Ready 是否可用。
func (c *WebClient) Ready() bool { return c.cfg.URL != "" }

var (
	webMu       sync.Mutex
	webBrowser  *rod.Browser
	webProc     *BrowserProc
	webProfile  string
	webHeadless bool
)

// getBrowser 取浏览器单例；请求的无头模式与现有实例不一致时会重启浏览器。
// 课堂分析一律无头，避免弹出窗口影响上课；只有登录流程才允许可见窗口。
func getBrowser(cfg config.WebAIConfig, profileDir string, headless bool) (*rod.Browser, error) {
	webMu.Lock()
	defer webMu.Unlock()
	if webBrowser != nil {
		if webHeadless == headless {
			return webBrowser, nil
		}
		// 模式不同：关掉重开。
		if webProc != nil && webProc.Cmd != nil && webProc.Cmd.Process != nil {
			_ = webProc.Cmd.Process.Kill()
		}
		_ = webBrowser.Close()
		webBrowser, webProc = nil, nil
	}
	proc, err := LaunchBrowser(cfg.BrowserPath, profileDir, headless)
	if err != nil {
		return nil, err
	}
	ws := proc.WS
	if ws == "" {
		ws = proc.URL
	}
	br := rod.New().ControlURL(ws)
	if err := br.Connect(); err != nil {
		// 复用残留实例时 proc.Cmd 为 nil，不能直接 Kill（会空指针拖垮整节课）。
		if proc.Cmd != nil && proc.Cmd.Process != nil {
			_ = proc.Cmd.Process.Kill()
		} else {
			killByPort(proc.Port)
		}
		// 清掉残留后当场重试一次全新启动
		proc2, err2 := LaunchBrowser(cfg.BrowserPath, profileDir, headless)
		if err2 != nil {
			return nil, fmt.Errorf("连接浏览器失败: %w", err)
		}
		ws2 := proc2.WS
		if ws2 == "" {
			ws2 = proc2.URL
		}
		br2 := rod.New().ControlURL(ws2)
		if err2 := br2.Connect(); err2 != nil {
			if proc2.Cmd != nil && proc2.Cmd.Process != nil {
				_ = proc2.Cmd.Process.Kill()
			} else {
				killByPort(proc2.Port)
			}
			return nil, fmt.Errorf("连接浏览器失败: %w", err2)
		}
		webBrowser = br2
		webProc = proc2
		webProfile = profileDir
		webHeadless = headless
		logx.Infof("webai", "浏览器已重启（无头=%v），调试端口 %d", headless, proc2.Port)
		return br2, nil
	}
	webBrowser = br
	webProc = proc
	webProfile = profileDir
	webHeadless = headless
	logx.Infof("webai", "浏览器已启动（无头=%v），调试端口 %d", headless, proc.Port)
	return br, nil
}

func (c *WebClient) ensureBrowser(profileDir string) (*rod.Browser, error) {
	// 调用模型时一律无头，绝不让浏览器窗口出现在课堂上。
	return getBrowser(c.cfg, profileDir, true)
}

// Close 关闭浏览器。
func (c *WebClient) Close() {
	webMu.Lock()
	defer webMu.Unlock()
	if webBrowser != nil {
		_ = webBrowser.Close()
		webBrowser = nil
	}
	if webProc != nil && webProc.Cmd != nil && webProc.Cmd.Process != nil {
		_ = webProc.Cmd.Process.Kill()
	}
	webProc = nil
	webHeadless = false
}

// Chat 在网页版中提问并读取回答。
func (c *WebClient) Chat(ctx context.Context, msgs []Message, opt Options) (*Result, error) {
	return c.Stream(ctx, msgs, opt, nil)
}

// newChat 点击“新对话”，开始一条干净的会话。
func newChat(page *rod.Page) {
	js := `() => {
		const sels = ['button[class*=new-chat]', 'a[class*=new-chat]', '[data-testid*=new-chat]',
			'button[aria-label*="New chat"]', 'button[aria-label*="新对话"]', 'div[class*=new-chat]'];
		for (const s of sels) {
			const b = document.querySelector(s);
			if (b) { b.click(); return s; }
		}
		// 退而求其次：找文字为「新对话 / New chat」的按钮
		for (const b of document.querySelectorAll('button, a, div[role=button]')) {
			const t = (b.innerText || '').trim();
			if (t === '新对话' || t === 'New chat' || t === '新建对话') { b.click(); return 'text:' + t; }
		}
		return '';
	}`
	res, err := page.Eval(js)
	if err == nil && res.Value.Str() != "" {
		logx.Debugf("webai", "已新建对话（%s）", res.Value.Str())
		time.Sleep(900 * time.Millisecond)
	}
}

// subjectChat 确保当前对话属于指定学科：换学科时开一条新对话。
// 同学科复用同一条，历史记录里每门课只有一条，不再堆垃圾。
func (c *WebClient) subjectChat(page *rod.Page, subject string) {
	c.mu.Lock()
	last := c.subject
	c.mu.Unlock()
	if subject == "" || subject == last {
		return
	}
	newChat(page)
	c.mu.Lock()
	c.subject = subject
	c.mu.Unlock()
}

// Stream 网页版没有真正的流式接口，这里用轮询模拟。
func (c *WebClient) Stream(ctx context.Context, msgs []Message, opt Options, onDelta func(string)) (*Result, error) {
	dir := c.dir
	c.mu.Lock()
	br, err := c.ensureBrowser(dir)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(c.cfg.ReplyTimeout) * time.Second
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 && d < timeout {
			timeout = d
		}
	}

	page, err := br.Page(proto.TargetCreateTarget{URL: c.cfg.URL})
	if err != nil {
		return nil, fmt.Errorf("打开页面失败: %w", err)
	}
	defer page.Close()

	page = page.Timeout(timeout)
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("页面加载超时: %w", err)
	}
	// 单页应用还需要时间渲染出输入框与会话容器。
	time.Sleep(2600 * time.Millisecond)
	dismissPopup(page)
	// 每门学科一条独立对话历史，避免把垃圾堆满侧边栏。
	c.subjectChat(page, opt.Subject)

	inputSel := c.sel("input", "textarea")
	el, err := page.Timeout(20 * time.Second).Element(inputSel)
	if err != nil {
		return nil, fmt.Errorf("找不到输入框（%s），请在设置中调整选择器", inputSel)
	}
	if err := el.ScrollIntoView(); err != nil {
		_ = err
	}
	_ = el.Click(proto.InputMouseButtonLeft, 1)

	prompt, images := flatten(msgs)
	// flatten 会把截图落到 %TEMP%，1.2.5 从不回收：一堂 19 页的课就在
	// 学生本机留下 19 张课堂画面，越积越多。这里用完即删。
	defer cleanupTempImages(images)
	if opt.FollowUp {
		prompt = slimPrompt(prompt)
	}
	if len(images) > 0 {
		if fileSel := c.sel("file", "input[type=file]"); fileSel != "" {
			if fi, err := page.Element(fileSel); err == nil {
				if err := fi.SetFiles([]string{images[0]}); err != nil {
					logx.Warnf("webai", "上传图片失败: %v", err)
				} else {
					time.Sleep(2500 * time.Millisecond)
				}
			}
		}
	}

	baseline := readBodyText(page)
	if err := el.Input(prompt); err != nil {
		return nil, fmt.Errorf("输入失败: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	if !c.send(page, el, prompt) {
		return nil, fmt.Errorf("消息未能发送，请检查网页版是否已登录")
	}

	// 轮询回复。
	replySel := c.sel("reply", "")
	var last string
	stableCount := 0
	deadline := time.Now().Add(timeout)
	started := false
	fromSelector := false
	begin := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(900 * time.Millisecond)
		raw, ok := readReply(page, replySel, baseline)
		cur := cleanReply(raw, prompt)
		if cur == "" {
			continue
		}
		// 回退路径拿到的正文可能包含侧栏等界面文字，需要额外过滤。
		if !ok && (len([]rune(cur)) < 120 || looksLikeChrome(cur)) {
			continue
		}
		started = true
		fromSelector = fromSelector || ok
		if strings.HasPrefix(cur, last) && len(cur) > len(last) && onDelta != nil {
			onDelta(strings.TrimPrefix(cur, last))
		}
		if cur == last && !generating(page) {
			stableCount++
			if stableCount >= 3 && time.Since(begin) > 3*time.Second && (fromSelector || len([]rune(cur)) > 150) {
				break
			}
		} else {
			stableCount = 0
		}
		last = cur
	}
	if !started || strings.TrimSpace(last) == "" {
		return nil, fmt.Errorf("等待网页版回答超时，请确认已登录且网络正常")
	}
	return &Result{Text: strings.TrimSpace(last), Via: "web", Model: "web:" + hostOf(c.cfg.URL)}, nil
}

// send 发送消息。部分站点发送后不会清空输入框，因此以"出现新的助手消息"或"正在生成"作为成功判据。
func (c *WebClient) send(page *rod.Page, el *rod.Element, prompt string) bool {
	before := assistantCount(page)
	click := func() {
		js := `() => {
			const sels = ['button.send-button', 'button[aria-label="Send"]', '.message-input-right-button-send',
				'button[class*=send]', 'button[type=submit]'];
			for (const s of sels) {
				const b = document.querySelector(s);
				if (b && !b.disabled) { b.click(); return s; }
			}
			return '';
		}`
		_, _ = page.Eval(js)
	}
	click()
	// 等待 6 秒观察是否开始生成。
	for i := 0; i < 12; i++ {
		time.Sleep(500 * time.Millisecond)
		if generating(page) || assistantCount(page) > before {
			return true
		}
	}
	// 再补一次真实点击（有的按钮需要鼠标事件）。
	if btn, err := page.Element("button.send-button"); err == nil {
		_ = btn.Click(proto.InputMouseButtonLeft, 1)
		for i := 0; i < 8; i++ {
			time.Sleep(500 * time.Millisecond)
			if generating(page) || assistantCount(page) > before {
				return true
			}
		}
	}
	if v, err := el.Property("value"); err == nil && strings.TrimSpace(v.Str()) == "" {
		return true
	}
	return false
}

// assistantCount 返回页面中助手气泡的数量，用于判断是否产生了新回复。
func assistantCount(page *rod.Page) int {
	res, err := page.Eval(`() => document.querySelectorAll('[class*=assistant]').length`)
	if err != nil {
		return 0
	}
	return int(res.Value.Num())
}

// dismissPopup 关闭可能阻挡操作的引导弹窗。
func dismissPopup(page *rod.Page) {
	_, _ = page.Eval(`() => {
		const keys = ['Not now', 'Maybe later', 'Close', 'Later', '暂不', '以后再说', '关闭'];
		const nodes = [...document.querySelectorAll('button, [role=button]')];
		for (const n of nodes) {
			const t = (n.innerText || n.getAttribute('aria-label') || '').trim();
			if (keys.some(k => t === k || t.includes(k))) { try { n.click(); } catch (e) {} }
		}
	}`)
}

func looksLikeChrome(s string) bool {
	markers := []string{"New Chat", "Upgrade", "All chats", "How can I help you", "Download App", "Qwen3", "Free", "用户协议"}
	n := 0
	for _, m := range markers {
		if strings.Contains(s, m) {
			n++
		}
	}
	return n >= 2
}

func (c *WebClient) sel(key, fallback string) string {
	if v, ok := c.cfg.Selectors[key]; ok && v != "" {
		return v
	}
	return fallback
}

func readBodyText(page *rod.Page) string {
	res, err := page.Eval(`() => document.body ? document.body.innerText : ''`)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

var replySels = []string{
	".qwen-chat-message-assistant .qwen-markdown",
	".qwen-markdown",
	".ds-markdown",
	".markdown-body",
	"[class*=assistant] [class*=markdown]",
	"[class*=markdown]",
}

func readReply(page *rod.Page, selectors string, baseline string) (string, bool) {
	sels := replySels
	if selectors != "" {
		sels = append(strings.Split(selectors, ","), sels...)
	}
	// 优先读取结构化容器里的最后一条。
	for _, sel := range sels {
		sel = strings.TrimSpace(sel)
		if sel == "" {
			continue
		}
		res, err := page.Eval(`(sel) => {
			try {
				const els = document.querySelectorAll(sel);
				if (!els.length) return '';
				return els[els.length-1].innerText || '';
			} catch (e) { return ''; }
		}`, sel)
		if err == nil {
			if s := strings.TrimSpace(res.Value.Str()); len([]rune(s)) >= 2 {
				return s, true
			}
		}
	}
	// 回退：取正文增量。
	body := readBodyText(page)
	if len(body) > len(baseline) {
		return strings.TrimSpace(body[len(baseline):]), false
	}
	return "", false
}

// generating 判断页面是否仍在生成（存在停止按钮）。
func generating(page *rod.Page) bool {
	res, err := page.Eval(`() => {
		const b = document.querySelector('button[aria-label*=Stop], button[class*=stop], .stop-btn');
		return !!b;
	}`)
	if err != nil {
		return false
	}
	return res.Value.Bool()
}

var noiseRe = regexp.MustCompile(`^(复制|重新生成|点赞|点踩|分享|编辑|展开|收起|复制全文|复制代码|喜欢|不喜欢|Regenerate|Copy|Edit|Like|Dislike|Share|Thinking completed|Thinking\.\.\.|思考完成|已深度思考.*|深度思考.*|Thought for .*|Reasoning.*)\s*$`)

// feedbackRe 匹配问答界面里的“帮助我们反馈”一类夹带文案。
var feedbackRe = regexp.MustCompile(`(?i)(帮助我们反馈|帮助改进|如回答(不准确|不符合预期)|对回答.{0,6}满意|回答是否(准确|有帮助)|AI ?生成内容|内容由 AI 生成|仅供参考|点击.{0,8}反馈|was this (answer|response) (helpful|accurate)|help us (improve|feedback)|generated by AI)`)

// cleanReply 清洗网页版抓到的回答：去界面噪声、去反馈夹带、去整段重复、修乱码。
func cleanReply(s, prompt string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = fixMojibake(s)

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if noiseRe.MatchString(t) || feedbackRe.MatchString(t) {
			continue
		}
		// 纯符号/按钮残留
		if isChromeGlyph(t) {
			continue
		}
		if strings.TrimSpace(prompt) != "" && strings.HasPrefix(t, strings.TrimSpace(prompt)) && len(out) == 0 {
			continue
		}
		out = append(out, l)
	}
	res := strings.TrimSpace(strings.Join(out, "\n"))
	res = dedupeBlocks(res)
	return res
}

// isChromeGlyph 判断是否只是界面图标/分隔符残留。
func isChromeGlyph(t string) bool {
	if len([]rune(t)) > 8 {
		return false
	}
	for _, r := range t {
		switch {
		case r >= 0x2190 && r <= 0x21FF: // 箭头
			return true
		case r >= 0x2500 && r <= 0x257F: // 制表符
			return true
		case r >= 0x2580 && r <= 0x259F: // 块元素
			return true
		case r >= 0x25A0 && r <= 0x25FF: // 几何图形
			return true
		case r >= 0x2600 && r <= 0x26FF: // 杂项符号
			return true
		case r >= 0x2700 && r <= 0x27BF: // 装饰符号
			return true
		}
	}
	return t == "|" || t == "·" || t == "•"
}

// dedupeBlocks 去掉整段重复的内容（网页版偶发把同一段回答吐两次）。
func dedupeBlocks(s string) string {
	if s == "" {
		return s
	}
	// 1) 连续完全相同的段落只保留一次。
	blocks := strings.Split(s, "\n\n")
	seen := make([]string, 0, len(blocks))
	for _, b := range blocks {
		t := strings.TrimSpace(b)
		if t == "" {
			continue
		}
		dup := false
		for _, prev := range seen {
			if prev == t {
				dup = true
				break
			}
		}
		if !dup {
			seen = append(seen, t)
		}
	}
	merged := strings.TrimSpace(strings.Join(seen, "\n\n"))

	// 2) 整体重复两遍（前半 == 后半）时取前半。
	r := []rune(merged)
	if len(r) >= 80 {
		half := len(r) / 2
		// 允许中间一个换行的偏差
		for d := -4; d <= 4; d++ {
			mid := half + d
			if mid <= 10 || mid >= len(r)-10 {
				continue
			}
			a := strings.TrimSpace(string(r[:mid]))
			b := strings.TrimSpace(string(r[mid:]))
			if a == b {
				return a
			}
			if len(a) > 40 && strings.HasPrefix(b, a) {
				return a
			}
		}
	}
	return merged
}

// looksMojibake 判断是否为典型的“UTF-8 被按 Latin-1 解码”乱码。
func looksMojibake(s string) bool {
	if strings.ContainsRune(s, '\uFFFD') {
		return true
	}
	rs := []rune(s)
	if len(rs) < 8 {
		return false
	}
	bad := 0
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if c < 0x80 {
			continue
		}
		// Ã/Â/å/é/æ 等 + 后面一个西欧字符，是 UTF-8 双字节被拆开的典型特征
		if strings.ContainsRune("ÃÂâæçåéïä½¥€š„Ÿ¼¾", c) {
			bad++
			continue
		}
		if c >= 0xE000 && c <= 0xF8FF {
			bad++
		}
	}
	return float64(bad)/float64(len(rs)) > 0.35
}

// fixMojibake 尝试把“UTF-8 字节被当 Latin-1”造成的乱码还原。
func fixMojibake(s string) string {
	if !looksMojibake(s) {
		return s
	}
	// Latin-1 / CP1252 反向映射（UTF-8 字节被按西欧编码解码的还原）
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r <= 0xFF {
			b = append(b, byte(r))
			continue
		}
		if v, ok := cp1252Rev[r]; ok {
			b = append(b, v)
			continue
		}
		return s // 有无法映射的字符，不做猜测
	}
	if fixed, err := decodeUTF8Strict(b); err == nil && !looksMojibake(fixed) && countCJK(fixed) >= countCJK(s) {
		return fixed
	}
	return strings.ReplaceAll(s, "\uFFFD", "")
}

// cp1252Rev 是 Windows-1252 里 C1 区（0x80-0x9F）的反向映射。
var cp1252Rev = map[rune]byte{
	0x20AC: 0x80, 0x201A: 0x82, 0x0192: 0x83, 0x201E: 0x84,
	0x2026: 0x85, 0x2020: 0x86, 0x2021: 0x87, 0x02C6: 0x88,
	0x2030: 0x89, 0x0160: 0x8A, 0x2039: 0x8B, 0x0152: 0x8C,
	0x017D: 0x8E, 0x2018: 0x91, 0x2019: 0x92, 0x201C: 0x93,
	0x201D: 0x94, 0x2022: 0x95, 0x2013: 0x96, 0x2014: 0x97,
	0x02DC: 0x98, 0x2122: 0x99, 0x0161: 0x9A, 0x203A: 0x9B,
	0x0153: 0x9C, 0x017E: 0x9E, 0x0178: 0x9F,
}

func decodeUTF8Strict(b []byte) (string, error) {
	for i := 0; i < len(b); {
		r, size := decodeRune(b[i:])
		if r == '\uFFFD' && size == 1 {
			return "", fmt.Errorf("invalid utf-8 at %d", i)
		}
		i += size
	}
	return string(b), nil
}

func decodeRune(b []byte) (rune, int) {
	r, size := utf8.DecodeRune(b)
	return r, size
}

func countCJK(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			n++
		}
	}
	return n
}

// slimPrompt 同一条对话里的后续提问：去掉重复的背景与要求说明，
// 只留本页要分析的内容（对话上下文里已经有前面的铺垫）。
// cleanupTempImages 删除为上传而生成的临时截图。
func cleanupTempImages(paths []string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		_ = os.Remove(p)
	}
}

// slimPrompt 只删掉历史回答，绝不删系统提示。
//
// 1.2.5 的实机故障（2026-11-02 地理课、物理课）：这里原本从「【任务】」处
// 截断，把整段系统提示——角色、硬性约束、判定锚点、以及最关键的 JSON 输出
// 格式——全部删掉，只留下「学科 / 页码 / 画面文字 / 语音转写」四段材料。
//
// 模型拿不到字段约定，就自己包了一层 {"content": "..."}：这是合法 JSON，
// 但 board_text / notes / summary 全部缺席，解析不报错、字段全空，
// 于是从第 2 页起报告里只剩一句「照搬评分 -1」。第 1 页之所以正常，
// 正因为它是本堂课第一次请求，走的是完整提示词。
//
// 另外，Stream 每次都用 br.Page 开新标签页，页面里本来就没有上一轮的上下文，
// 「系统提示只发一次」这个前提从一开始就不成立。
func slimPrompt(p string) string {
	lines := strings.Split(p, "\n")
	out := make([]string, 0, len(lines))
	skipHistory := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "【上一轮回答】") {
			skipHistory = true
			continue
		}
		if skipHistory {
			// 历史块到下一个小标题为止。
			if strings.HasPrefix(t, "【") {
				skipHistory = false
			} else {
				continue
			}
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// flatten 把消息压平成一段提示词，并把图片写入临时文件供上传。
func flatten(msgs []Message) (string, []string) {
	var sb strings.Builder
	var images []string
	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			sb.WriteString("【要求】\n")
		case RoleAssistant:
			sb.WriteString("\n【上一轮回答】\n")
		}
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				sb.WriteString(p.Text)
				sb.WriteString("\n")
			case "image":
				if p.ImageB64 == "" {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(p.ImageB64)
				if err != nil {
					continue
				}
				ext := ".png"
				if strings.Contains(p.MIME, "jpeg") {
					ext = ".jpg"
				}
				f, err := os.CreateTemp("", "classnote-slide-*"+ext)
				if err != nil {
					continue
				}
				if _, err := f.Write(raw); err == nil {
					images = append(images, f.Name())
				}
				_ = f.Close()
				sb.WriteString("（请结合附图作答）\n")
			}
		}
		if m.Role == RoleSystem {
			sb.WriteString("\n【任务】\n")
		}
	}
	return strings.TrimSpace(sb.String()), images
}

func findBrowser() string {
	bases := []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")}
	var cands []string
	for _, b := range bases {
		if b == "" {
			continue
		}
		cands = append(cands,
			filepath.Join(b, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(b, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// OpenLogin 打开可见浏览器，供用户登录。
func OpenLogin(cfg config.Config, profileDir string) error {
	// 登录需要看见窗口，这里显式用可见模式。
	br, err := getBrowser(cfg.WebAI, profileDir, false)
	if err != nil {
		return err
	}
	page, err := br.Page(proto.TargetCreateTarget{URL: cfg.WebAI.URL})
	if err != nil {
		return err
	}
	_ = page.Timeout(30 * time.Second).WaitLoad()
	return nil
}

// LoginCheck 判断页面是否已登录（粗略判断：存在输入框）。
func LoginCheck(cfg config.Config, profileDir string) (bool, string, error) {
	br, err := getBrowser(cfg.WebAI, profileDir, false)
	if err != nil {
		return false, "", err
	}
	page, err := br.Page(proto.TargetCreateTarget{URL: cfg.WebAI.URL})
	if err != nil {
		return false, "", err
	}
	defer page.Close()
	page = page.Timeout(30 * time.Second)
	if err := page.WaitLoad(); err != nil {
		return false, "", err
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := page.Element("textarea"); err == nil {
		return true, "已检测到输入框，登录状态可用", nil
	}
	return false, "未检测到输入框，可能未登录或页面结构变化", nil
}
