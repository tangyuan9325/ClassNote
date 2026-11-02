package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// WebChat 是一门学科在网页版 AI 上固定的那条对话。
//
// 1.2.6 之前每页分析都 br.Page(URL) 开一个新标签页再关掉，页面里没有任何
// 上下文，所谓「同学科复用一条对话」其实从没生效：每页都是一次全新会话，
// 既要把系统提示重发一遍（贵、慢），也让模型无法承接上一页的内容。
//
// 这里把对话地址持久化下来，之后同一门课永远打开同一个 URL，系统提示只写
// 一次，后续每页只发材料。
type WebChat struct {
	Subject   string `json:"subject"`
	URL       string `json:"url"`
	Title     string `json:"title,omitempty"`
	Primed    bool   `json:"primed"`             // 系统提示是否已经写进这条对话
	PrimedAt  string `json:"primedAt,omitempty"` //
	UpdatedAt string `json:"updatedAt,omitempty"`
	Source    string `json:"source,omitempty"` // prewarm | sidebar | manual | runtime
	Sessions  int    `json:"sessions,omitempty"`
}

// chatTag 是对话指纹的前缀，写在开场消息第一行。
// 网页版会把首句当成会话标题，于是侧边栏里就能看到「【ClassNote·语文】…」，
// 下次启动靠它把对话认回来，比匹配科目名可靠得多。
const chatTag = "ClassNote"

// chatTagLine 返回这门课的对话指纹。
func chatTagLine(subject string) string {
	return "【" + chatTag + "·" + subject + "】"
}

// subjectKey 归一化学科名作为存储键。
func subjectKey(subject string) string {
	s := strings.TrimSpace(subject)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\u3000", "")
	return strings.ToLower(s)
}

// webChatStore 按学科持久化对话地址。
type webChatStore struct {
	mu     sync.Mutex
	path   string
	data   map[string]WebChat
	loaded bool
}

func newWebChatStore(path string) *webChatStore {
	s := &webChatStore{path: path, data: map[string]WebChat{}}
	s.load()
	return s
}

func (s *webChatStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	s.loaded = true
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []WebChat
	if json.Unmarshal(raw, &list) != nil {
		// 兼容早期写成 map 的形式。
		var m map[string]WebChat
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		for _, v := range m {
			list = append(list, v)
		}
	}
	for _, c := range list {
		if k := subjectKey(c.Subject); k != "" {
			s.data[k] = c
		}
	}
}

func (s *webChatStore) saveLocked() {
	if s.path == "" {
		return
	}
	list := make([]WebChat, 0, len(s.data))
	for _, v := range s.data {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Subject < list[j].Subject })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	// 先写临时文件再改名：中途断电不会留下半个 JSON，
	// 否则下次启动会把所有科目的对话地址一起丢掉。
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

func (s *webChatStore) get(subject string) (WebChat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data[subjectKey(subject)]
	return c, ok
}

func (s *webChatStore) put(c WebChat) {
	if subjectKey(c.Subject) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.UpdatedAt = time.Now().Format(time.RFC3339)
	s.data[subjectKey(c.Subject)] = c
	s.saveLocked()
}

func (s *webChatStore) touch(subject, url, title string, primed bool, source string) WebChat {
	c, ok := s.get(subject)
	if !ok {
		c = WebChat{Subject: subject}
	}
	if url != "" {
		c.URL = url
	}
	if title != "" {
		c.Title = title
	}
	if primed && !c.Primed {
		c.Primed = true
		c.PrimedAt = time.Now().Format(time.RFC3339)
	}
	if c.Source == "" || source != "" {
		c.Source = source
	}
	s.put(c)
	return c
}

func (s *webChatStore) delete(subject string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := subjectKey(subject)
	if _, ok := s.data[k]; !ok {
		return false
	}
	delete(s.data, k)
	s.saveLocked()
	return true
}

func (s *webChatStore) all() []WebChat {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WebChat, 0, len(s.data))
	for _, v := range s.data {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

// primeMessage 构造写进对话的开场白：固定指纹 + 系统提示 + 一句确认。
//
// 之所以要把整段系统提示塞在这里，是因为网页版看不到「系统消息」这个概念，
// 只有普通对话消息。写一次之后，后续每页只发材料，模型靠对话上下文维持角色。
func primeMessage(subject, system string) string {
	var sb strings.Builder
	sb.WriteString(chatTagLine(subject))
	sb.WriteString("这门课（")
	sb.WriteString(subject)
	sb.WriteString("）的课堂记录专用对话，后续所有页面都发在这里。\n\n")
	sb.WriteString(strings.TrimSpace(system))
	sb.WriteString("\n\n之后我会按页发送材料（页码、停留时长、画面文字、语音转写、上一页要点），")
	sb.WriteString("你只需要直接返回上面约定的 JSON，不要再加解释。")
	sb.WriteString("现在请只回复四个字：已就绪。")
	return sb.String()
}

// chatMenuJS 扫描页面里的会话链接，返回 [{href,title}] 的 JSON 字符串。
//
// 各家站点的会话 id 位置不一样（qwen 是 /c/<id>，deepseek 是 /a/chat/s/<id>），
// 用一条宽松的正则把它们一起兜住；标题长度截断，避免把整段正文当标题。
const chatMenuJS = `() => {
	const re = /\/(?:c|chat|conversation|s|a\/chat\/s)\/([A-Za-z0-9_-]{8,})/;
	const out = [];
	const seen = new Set();
	for (const a of document.querySelectorAll('a[href]')) {
		let href = a.getAttribute('href') || '';
		if (!re.test(href)) continue;
		try { href = new URL(href, location.origin).href; } catch (e) { continue; }
		if (seen.has(href)) continue;
		seen.add(href);
		let t = (a.innerText || a.getAttribute('title') || a.getAttribute('aria-label') || '').trim();
		t = (t.split('\n')[0] || '').trim();
		if (t.length > 80) t = t.slice(0, 80);
		out.push({ href: href, title: t });
	}
	return JSON.stringify(out.slice(0, 100));
}`

// sideChat 是侧边栏里的一条历史会话。
type sideChat struct {
	Href  string `json:"href"`
	Title string `json:"title"`
}

// pickChat 从侧边栏里挑出属于这门课的那条。
//
// 优先级：带指纹的 > 标题里含科目名的。第二条是给「用户自己在网页里建的对话」
// 留的口子，但要求标题不能太长，否则匹配到的往往是某段正文。
func pickChat(items []sideChat, subject string) (sideChat, string) {
	key := subjectKey(subject)
	if key == "" {
		return sideChat{}, ""
	}
	tag := strings.ToLower(chatTagLine(subject))
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Title), tag) {
			return it, "fingerprint"
		}
	}
	// 标题里带学科名，且不像一整段正文。
	for _, it := range items {
		t := subjectKey(it.Title)
		if strings.Contains(t, key) && len([]rune(it.Title)) <= 40 {
			return it, "subject"
		}
	}
	return sideChat{}, ""
}

// errNoChatStore 表示对话存储不可用。
func (c *WebClient) chatStore() *webChatStore {
	if c.chats == nil {
		c.chats = newWebChatStore("")
	}
	return c.chats
}

// Chats 返回所有已记录的对话。
func (c *WebClient) Chats() []WebChat { return c.chatStore().all() }

// SetChat 手动指定某门课的对话地址。
func (c *WebClient) SetChat(subject, url string) error {
	subject = strings.TrimSpace(subject)
	url = strings.TrimSpace(url)
	if subject == "" {
		return fmt.Errorf("学科不能为空")
	}
	if url == "" {
		c.chatStore().delete(subject)
		return nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("对话地址必须以 http:// 或 https:// 开头")
	}
	// 手动填的地址无法确认提示词写没写过：标为未预热，
	// 第一次分析前会自动补一条开场消息。
	c.chatStore().put(WebChat{
		Subject: subject, URL: url, Primed: false, Source: "manual",
	})
	return nil
}

// ClearChat 忘掉某门课的对话（下一次会重新建）。
func (c *WebClient) ClearChat(subject string) bool {
	return c.chatStore().delete(subject)
}
