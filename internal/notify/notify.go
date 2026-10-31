// Package notify 把课堂事件推送到常用的机器人/群聊。
//
// 支持：企业微信群机器人、钉钉群机器人、飞书群机器人、Telegram Bot、Server酱、自定义 Webhook。
// 参考 BetterGI 的通知模块思路：一个事件 → 多个渠道，每个渠道可单独开关。
package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"classnote/internal/config"
	"classnote/internal/logx"
)

// Event 是一次通知。
type Event struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Level string `json:"level"` // info | warn | error
}

// Notifier 按配置把事件发到各渠道。
type Notifier struct {
	cfg config.NotifyConfig
}

// New 创建通知器。
func New(cfg config.NotifyConfig) *Notifier { return &Notifier{cfg: cfg} }

// Update 更新配置。
func (n *Notifier) Update(cfg config.NotifyConfig) { n.cfg = cfg }

// Enabled 是否启用。
func (n *Notifier) Enabled() bool { return n.cfg.Enabled && len(n.cfg.Providers) > 0 }

// Send 发送一条通知（异步、失败只记日志）。
func (n *Notifier) Send(ev Event) {
	if !n.Enabled() {
		return
	}
	if ev.Level == "" {
		ev.Level = "info"
	}
	for _, p := range n.cfg.Providers {
		if !p.Enabled {
			continue
		}
		// 事件过滤：Providers.Events 为空表示接收全部
		if len(p.Events) > 0 && !contains(p.Events, ev.Level) && !contains(p.Events, "*") {
			continue
		}
		prov := p
		go func() {
			if err := deliver(prov, ev); err != nil {
				logx.Warnf("notify", "%s 通知失败: %v", prov.Name, err)
			} else {
				logx.Debugf("notify", "%s 通知已发送", prov.Name)
			}
		}()
	}
}

// Test 发送测试消息并返回每个渠道的结果。
func (n *Notifier) Test() map[string]string {
	out := map[string]string{}
	ev := Event{Title: "ClassNote 测试通知", Text: "这是一条测试消息，收到说明该渠道可用。", Level: "info"}
	for _, p := range n.cfg.Providers {
		if !p.Enabled {
			out[p.Name] = "已禁用，跳过"
			continue
		}
		if err := deliver(p, ev); err != nil {
			out[p.Name] = "失败：" + err.Error()
		} else {
			out[p.Name] = "成功"
		}
	}
	if len(out) == 0 {
		out["（未配置）"] = "请先添加通知渠道"
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

var cli = &http.Client{Timeout: 15 * time.Second}

func deliver(p config.NotifyProvider, ev Event) error {
	switch strings.ToLower(p.Type) {
	case "wecom", "企业微信":
		return sendWeCom(p, ev)
	case "dingtalk", "钉钉":
		return sendDingTalk(p, ev)
	case "feishu", "lark", "飞书":
		return sendFeishu(p, ev)
	case "telegram":
		return sendTelegram(p, ev)
	case "serverchan", "server酱":
		return sendServerChan(p, ev)
	case "discord":
		return sendDiscord(p, ev)
	case "slack":
		return sendSlack(p, ev)
	case "bark":
		return sendBark(p, ev)
	case "pushplus":
		return sendPushPlus(p, ev)
	case "ntfy":
		return sendNtfy(p, ev)
	case "gotify":
		return sendGotify(p, ev)
	case "webhook", "custom", "":
		return sendWebhook(p, ev)
	default:
		return fmt.Errorf("未知的通知类型 %q", p.Type)
	}
}

func postJSON(rawURL string, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := cli.Post(rawURL, "application/json", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func sendWeCom(p config.NotifyProvider, ev Event) error {
	if p.Webhook == "" {
		return fmt.Errorf("缺少 Webhook 地址")
	}
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{
			"content": fmt.Sprintf("### %s\n%s", ev.Title, ev.Text),
		},
	}
	// 加签模式
	if p.Secret != "" {
		ts := time.Now().Unix()
		stringToSign := fmt.Sprintf("%d\n%s", ts, p.Secret)
		mac := hmac.New(sha256.New, []byte(p.Secret))
		mac.Write([]byte(stringToSign))
		sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		p.Webhook = fmt.Sprintf("%s&timestamp=%d&sign=%s", p.Webhook, ts, url.QueryEscape(sign))
	}
	return postJSON(p.Webhook, payload)
}

func sendDingTalk(p config.NotifyProvider, ev Event) error {
	if p.Webhook == "" {
		return fmt.Errorf("缺少 Webhook 地址")
	}
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{
			"title": ev.Title,
			"text":  fmt.Sprintf("### %s\n%s", ev.Title, ev.Text),
		},
	}
	if p.Secret != "" {
		ts := time.Now().UnixMilli()
		stringToSign := fmt.Sprintf("%d\n%s", ts, p.Secret)
		mac := hmac.New(sha256.New, []byte(p.Secret))
		mac.Write([]byte(stringToSign))
		sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		p.Webhook = fmt.Sprintf("%s&timestamp=%d&sign=%s", p.Webhook, ts, sign)
	}
	return postJSON(p.Webhook, payload)
}

func sendFeishu(p config.NotifyProvider, ev Event) error {
	if p.Webhook == "" {
		return fmt.Errorf("缺少 Webhook 地址")
	}
	return postJSON(p.Webhook, map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"title":    map[string]any{"tag": "plain_text", "content": ev.Title},
				"template": map[bool]string{true: "red", false: "blue"}[ev.Level == "error"],
			},
			"elements": []any{
				map[string]any{"tag": "markdown", "content": ev.Text},
			},
		},
	})
}

func sendTelegram(p config.NotifyProvider, ev Event) error {
	if p.Token == "" || p.ChatID == "" {
		return fmt.Errorf("缺少 Token 或 ChatID")
	}
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", p.Token)
	return postJSON(u, map[string]any{
		"chat_id":    p.ChatID,
		"text":       fmt.Sprintf("*%s*\n%s", ev.Title, ev.Text),
		"parse_mode": "Markdown",
	})
}

func sendServerChan(p config.NotifyProvider, ev Event) error {
	key := p.Token
	if key == "" {
		return fmt.Errorf("缺少 SendKey")
	}
	u := fmt.Sprintf("https://sctapi.ftqq.com/%s.send", key)
	form := url.Values{}
	form.Set("title", ev.Title)
	form.Set("desp", ev.Text)
	resp, err := cli.Post(u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func sendWebhook(p config.NotifyProvider, ev Event) error {
	if p.Webhook == "" {
		return fmt.Errorf("缺少 Webhook 地址")
	}
	payload := map[string]any{
		"source":  "ClassNote",
		"title":   ev.Title,
		"text":    ev.Text,
		"level":   ev.Level,
		"time":    time.Now().Format(time.RFC3339),
		"channel": p.Name,
	}
	return postJSON(p.Webhook, payload)
}

func sendDiscord(p config.NotifyProvider, ev Event) error {
	if p.Webhook == "" {
		return fmt.Errorf("缺少 Discord Webhook 地址")
	}
	return postJSON(p.Webhook, map[string]any{
		"username": "ClassNote",
		"content":  fmt.Sprintf("**%s**\n%s", ev.Title, ev.Text),
	})
}

func sendSlack(p config.NotifyProvider, ev Event) error {
	if p.Webhook == "" {
		return fmt.Errorf("缺少 Slack Webhook 地址")
	}
	return postJSON(p.Webhook, map[string]any{
		"text": fmt.Sprintf("*%s*\n%s", ev.Title, ev.Text),
	})
}

func sendBark(p config.NotifyProvider, ev Event) error {
	// Bark：iOS 推送。token 放 DeviceKey，或直接把完整地址填在 Webhook。
	key := p.Token
	if key == "" {
		return fmt.Errorf("缺少 Bark DeviceKey")
	}
	u := fmt.Sprintf("https://api.day.app/%s", key)
	return postJSON(u, map[string]any{
		"title": ev.Title,
		"body":  ev.Text,
		"group": "ClassNote",
	})
}

func sendPushPlus(p config.NotifyProvider, ev Event) error {
	key := p.Token
	if key == "" {
		return fmt.Errorf("缺少 PushPlus Token")
	}
	return postJSON("https://www.pushplus.plus/send", map[string]any{
		"token":   key,
		"title":   ev.Title,
		"content": ev.Text,
	})
}

func sendNtfy(p config.NotifyProvider, ev Event) error {
	// ntfy：topic 地址填 Webhook（如 https://ntfy.sh/my-topic）
	if p.Webhook == "" {
		return fmt.Errorf("缺少 ntfy 主题地址")
	}
	req, err := http.NewRequest("POST", p.Webhook, strings.NewReader(ev.Text))
	if err != nil {
		return err
	}
	req.Header.Set("Title", ev.Title)
	req.Header.Set("Priority", map[bool]string{true: "high", false: "default"}[ev.Level != "info"])
	req.Header.Set("Tags", "ClassNote")
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func sendGotify(p config.NotifyProvider, ev Event) error {
	// Gotify：服务器地址填 Webhook（…/），token 放 Token
	if p.Webhook == "" || p.Token == "" {
		return fmt.Errorf("缺少 Gotify 地址或 Token")
	}
	u := strings.TrimRight(p.Webhook, "/") + "/message?token=" + p.Token
	return postJSON(u, map[string]any{
		"title":    ev.Title,
		"message":  ev.Text,
		"priority": map[bool]int{true: 8, false: 5}[ev.Level != "info"],
	})
}

// Presets 返回内置渠道模板，便于界面一键添加。
func Presets() []map[string]string {
	return []map[string]string{
		{"id": "wecom", "name": "企业微信群机器人", "hint": "群设置 → 群机器人 → 添加机器人，复制 Webhook；可选加签密钥"},
		{"id": "dingtalk", "name": "钉钉群机器人", "hint": "群设置 → 智能群助手 → 添加机器人，安全设置选“加签”，填入密钥"},
		{"id": "feishu", "name": "飞书群机器人", "hint": "群设置 → 群机器人 → 添加自定义机器人，复制 Webhook"},
		{"id": "telegram", "name": "Telegram Bot", "hint": "找 @BotFather 建 bot 拿 Token，ChatID 可用 @userinfobot 查询"},
		{"id": "serverchan", "name": "Server酱", "hint": "sct.ftqq.com 获取 SendKey，推送到微信"},
		{"id": "discord", "name": "Discord", "hint": "频道设置 → 整合 → Webhook，复制地址"},
		{"id": "slack", "name": "Slack", "hint": "api.slack.com/messaging/webhooks 创建 Incoming Webhook"},
		{"id": "bark", "name": "Bark（iOS）", "hint": "App 内复制 DeviceKey，推送到 iPhone"},
		{"id": "pushplus", "name": "PushPlus", "hint": "pushplus.plus 获取 Token，推送到微信"},
		{"id": "ntfy", "name": "ntfy", "hint": "填主题地址如 https://ntfy.sh/my-topic，手机装 ntfy App 订阅"},
		{"id": "gotify", "name": "Gotify", "hint": "自建推送服务，填服务器地址 + App Token"},
		{"id": "webhook", "name": "自定义 Webhook", "hint": "以 JSON POST 到你的地址：{source,title,text,level,time}"},
	}
}
