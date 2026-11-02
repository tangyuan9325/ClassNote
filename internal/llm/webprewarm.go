package llm

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"classnote/internal/logx"
)

// PrewarmResult 是一门课的对话准备结果。
type PrewarmResult struct {
	Subject string `json:"subject"`
	Action  string `json:"action"` // reused | adopted | created | failed
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
	At      string `json:"at,omitempty"`
}

// perPage 表示按页新开对话的旧模式。
func (c *WebClient) perPage() bool {
	return strings.EqualFold(strings.TrimSpace(c.cfg.ChatMode), "per-page")
}

// Prewarm 为每门课准备一条固定对话，并把系统提示写进去。
//
// 这一步放在「配置完成后的第一次启动」，之后整台机器上每门课都只有一条会话：
// 系统提示不再每页重发，模型也能顺着前几页的上下文往下讲。
//
// force=true 时无视已有记录重建（对应用户点「重新准备」）。
func (c *WebClient) Prewarm(ctx context.Context, subjects []string, system string, force bool) []PrewarmResult {
	out := make([]PrewarmResult, 0, len(subjects))
	if c.perPage() {
		return append(out, PrewarmResult{
			Action: "skipped", Message: "当前是「每页新开对话」模式，不需要准备对话",
		})
	}
	if len(subjects) == 0 {
		return out
	}
	br, err := c.ensureBrowser(c.dir)
	if err != nil {
		return append(out, PrewarmResult{Action: "failed", Message: "浏览器启动失败: " + err.Error()})
	}
	for _, subj := range subjects {
		subj = strings.TrimSpace(subj)
		if subj == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			out = append(out, PrewarmResult{Subject: subj, Action: "failed", Message: "已取消"})
			continue
		}
		res := c.prepareChat(br, subj, system, force)
		res.At = time.Now().Format("15:04:05")
		logx.Infof("webai", "准备对话 %s：%s %s", subj, res.Action, res.Message)
		out = append(out, res)
	}
	return out
}

// prepareChat 为一门课找到或建立固定对话，并确保系统提示已写入。
func (c *WebClient) prepareChat(br *rod.Browser, subject, system string, force bool) PrewarmResult {
	res := PrewarmResult{Subject: subject}

	if old, ok := c.chatStore().get(subject); ok && old.URL != "" && old.Primed && !force {
		if c.probeChat(br, old.URL) {
			res.Action = "reused"
			res.URL = old.URL
			res.Title = old.Title
			res.Message = "沿用已有对话（已写入提示词）"
			return res
		}
		logx.Warnf("webai", "%s 的旧对话打不开，重新准备", subject)
	}

	page, err := br.Page(proto.TargetCreateTarget{URL: c.cfg.URL})
	if err != nil {
		res.Action = "failed"
		res.Message = "打开网页版失败: " + err.Error()
		return res
	}
	defer page.Close()
	page = page.Timeout(150 * time.Second)
	if err := page.WaitLoad(); err != nil {
		res.Action = "failed"
		res.Message = "页面加载超时: " + err.Error()
		return res
	}
	time.Sleep(2600 * time.Millisecond)
	dismissPopup(page)

	// 先看侧边栏里有没有现成的：用户很可能自己建过对话，
	// 或者这是重装程序后第二次预热。
	hit, why := pickChat(c.sidebar(page), subject)
	if hit.Href != "" {
		if why == "fingerprint" {
			c.chatStore().put(WebChat{
				Subject: subject, URL: hit.Href, Title: hit.Title,
				Primed: true, PrimedAt: time.Now().Format(time.RFC3339), Source: "sidebar",
			})
			res.Action = "adopted"
			res.URL = hit.Href
			res.Title = hit.Title
			res.Message = "复用侧边栏里的本课对话（指纹匹配）"
			return res
		}
		// 只是标题里带科目名：不确定有没有系统提示，补一条进去。
		if c.openAndSend(br, hit.Href, primeMessage(subject, system)) {
			c.chatStore().put(WebChat{
				Subject: subject, URL: hit.Href, Title: hit.Title,
				Primed: true, PrimedAt: time.Now().Format(time.RFC3339), Source: "sidebar",
			})
			res.Action = "adopted"
			res.URL = hit.Href
			res.Title = hit.Title
			res.Message = "复用了标题含「" + subject + "」的对话，并补写提示词"
			return res
		}
	}

	// 没有可复用的，新建一条。
	newChat(page)
	time.Sleep(900 * time.Millisecond)
	if !c.sendText(page, primeMessage(subject, system)) {
		res.Action = "failed"
		res.Message = "开场提示发送失败，请确认网页版已登录"
		return res
	}
	// 等会话地址跳转出来：不同站点快慢不一，最多等 15 秒。
	url := ""
	for i := 0; i < 15; i++ {
		if u := pageURL(page); isChatURL(u) {
			url = u
			break
		}
		time.Sleep(time.Second)
	}
	if url == "" {
		res.Action = "failed"
		res.Message = "站点没有生成会话地址，无法固定对话（下次仍会用完整提示词）"
		return res
	}
	c.chatStore().put(WebChat{
		Subject: subject, URL: url, Primed: true,
		PrimedAt: time.Now().Format(time.RFC3339), Source: "prewarm",
	})
	res.Action = "created"
	res.URL = url
	res.Message = "已新建对话并写入提示词"
	return res
}

// openAndSend 打开一条已有对话并补发一段消息。
func (c *WebClient) openAndSend(br *rod.Browser, url, text string) bool {
	page, err := br.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return false
	}
	defer page.Close()
	page = page.Timeout(90 * time.Second)
	if err := page.WaitLoad(); err != nil {
		return false
	}
	time.Sleep(2400 * time.Millisecond)
	dismissPopup(page)
	return c.sendText(page, text)
}

// probeChat 检查一条对话地址是否还能打开（输入框存在）。
func (c *WebClient) probeChat(br *rod.Browser, url string) bool {
	page, err := br.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return false
	}
	defer page.Close()
	page = page.Timeout(25 * time.Second)
	if err := page.WaitLoad(); err != nil {
		return false
	}
	time.Sleep(1600 * time.Millisecond)
	_, err = page.Timeout(10 * time.Second).Element(c.sel("input", "textarea"))
	return err == nil
}

// sendText 在当前页面输入并发送一段文本。
func (c *WebClient) sendText(page *rod.Page, text string) bool {
	el, err := page.Timeout(20 * time.Second).Element(c.sel("input", "textarea"))
	if err != nil {
		return false
	}
	if err := el.ScrollIntoView(); err != nil {
		_ = err
	}
	_ = el.Click(proto.InputMouseButtonLeft, 1)
	if err := el.Input(text); err != nil {
		return false
	}
	time.Sleep(600 * time.Millisecond)
	return c.send(page, el, text)
}

// sidebar 读取侧边栏里的历史会话。
func (c *WebClient) sidebar(page *rod.Page) []sideChat {
	res, err := page.Eval(chatMenuJS)
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(res.Value.Str())
	if raw == "" || raw == "[]" {
		return nil
	}
	var items []sideChat
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return items
}

// ChatStatus 汇总「每科一条对话」的当前状态，供设置页展示。
type ChatStatus struct {
	Mode  string          `json:"mode"`
	File  string          `json:"file"`
	Chats []WebChat       `json:"chats"`
	Hint  string          `json:"hint,omitempty"`
	Busy  bool            `json:"busy"`
	Last  []PrewarmResult `json:"last,omitempty"`
}

// ChatStatus 返回对话配置概览。
func (c *WebClient) ChatStatus() ChatStatus {
	mode := "per-subject"
	if c.perPage() {
		mode = "per-page"
	}
	c.mu.Lock()
	busy, last := c.busy, c.last
	c.mu.Unlock()
	st := ChatStatus{Mode: mode, File: c.chatStore().path, Chats: c.chatStore().all(), Busy: busy, Last: last}
	if mode == "per-page" {
		st.Hint = "当前每页都会新开对话，模型看不到前面几页的内容；改成「每科一条对话」可以省掉重复提示词。"
	}
	return st
}

// priming 表示后台正在准备对话。
func (c *WebClient) priming() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy
}

// SetPriming 标记准备状态。
func (c *WebClient) SetPriming(v bool, last []PrewarmResult) {
	c.mu.Lock()
	c.busy = v
	c.last = last
	c.mu.Unlock()
}

// LastPrewarm 返回上一次预热结果。
func (c *WebClient) LastPrewarm() []PrewarmResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}
