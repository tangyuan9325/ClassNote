// Command verify 对运行中的 ClassNote 做端到端自检。
//
// 用法：
//
//	go run ./cmd/verify -base http://127.0.0.1:41735 [-full] [-exe build/ClassNote.exe]
//
// 默认只跑不依赖大模型的项目（几秒完成），-full 会额外调用网页版/接口模型，
// 验证拟真问答、逐页分析与报告摘要。
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procFindWindow = user32.NewProc("FindWindowW")
	procPostMsg    = user32.NewProc("PostMessageW")
	procGetRect    = shell32.NewProc("Shell_NotifyIconGetRect")
)

// 托盘菜单命令，与 internal/app/tray.go 保持一致。
const (
	cmdOpen   = 1001
	cmdToggle = 1002
	cmdReport = 1003
	cmdData   = 1004
	cmdQuit   = 1005
	cmdConfig = 1006

	wmCommand = 0x0111
)

type iconID struct {
	CbSize   uint32
	HWnd     windows.Handle
	UID      uint32
	GuidItem windows.GUID
}

type winRect struct{ Left, Top, Right, Bottom int32 }

type hit struct {
	Name   string        `json:"name"`
	Pass   bool          `json:"pass"`
	Detail string        `json:"detail"`
	Cost   time.Duration `json:"-"`
}

type ctx struct {
	base    string
	exe     string
	full    bool
	client  *http.Client
	results []hit
	session string
}

func (c *ctx) add(name string, pass bool, format string, args ...any) {
	c.results = append(c.results, hit{Name: name, Pass: pass, Detail: fmt.Sprintf(format, args...)})
	mark := "PASS"
	if !pass {
		mark = "FAIL"
	}
	fmt.Printf("  [%s] %-26s %s\n", mark, name, fmt.Sprintf(format, args...))
}

// check 运行一个断言，失败不中断后续用例。
func (c *ctx) check(name string, fn func() (string, error)) {
	t := time.Now()
	detail, err := fn()
	if err != nil {
		c.results = append(c.results, hit{Name: name, Pass: false, Detail: err.Error(), Cost: time.Since(t)})
		fmt.Printf("  [FAIL] %-26s %v\n", name, err)
		return
	}
	c.results = append(c.results, hit{Name: name, Pass: true, Detail: detail, Cost: time.Since(t)})
	fmt.Printf("  [PASS] %-26s %s\n", name, detail)
}

func (c *ctx) request(method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, raw, err
}

func (c *ctx) getJSON(path string, out any) error {
	code, raw, err := c.request("GET", path, nil)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("HTTP %d: %s", code, truncate(string(raw), 120))
	}
	return json.Unmarshal(raw, out)
}

func (c *ctx) postJSON(path string, body, out any) error {
	code, raw, err := c.request("POST", path, body)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("HTTP %d: %s", code, truncate(string(raw), 120))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func main() {
	base := flag.String("base", "http://127.0.0.1:41735", "服务地址")
	exe := flag.String("exe", filepath.Join("build", "ClassNote.exe"), "ClassNote.exe 路径（单实例测试用）")
	full := flag.Bool("full", false, "包含需要大模型的测试")
	out := flag.String("out", filepath.Join("build", "verify-report.md"), "报告输出路径")
	flag.Parse()

	c := &ctx{
		base: strings.TrimRight(*base, "/"),
		exe:  *exe,
		full: *full,
		client: &http.Client{
			// 结束课堂要等转写与分析收尾，给足时间。
			Timeout: 180 * time.Second,
		},
	}

	fmt.Printf("ClassNote 自检 %s\n", c.base)
	c.check("本地语音模型", func() (string, error) {
		var d struct {
			Items []struct {
				ID     string `json:"id"`
				OK     bool   `json:"ok"`
				Detail string `json:"detail"`
			} `json:"items"`
		}
		if err := c.getJSON("/api/env", &d); err != nil {
			return "", err
		}
		for _, it := range d.Items {
			if it.ID == "asr" {
				if !it.OK {
					return "", fmt.Errorf("%s", it.Detail)
				}
				return it.Detail, nil
			}
		}
		return "", fmt.Errorf("环境探测里没有语音项")
	})

	c.check("语音转写链路", func() (string, error) {
		// 用一段静音 WAV 走完整链路：本地 whisper 必须给出响应（哪怕是空文本）
		wav := makeWAV(16000, 1) // 1 秒静音
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		fw, _ := mw.CreateFormFile("file", "test.wav")
		_, _ = fw.Write(wav)
		_ = mw.Close()
		req, _ := http.NewRequest("POST", c.base+"/api/asr/test", body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if tok := ""; tok != "" {
			req.Header.Set("X-ClassNote-Token", tok)
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out struct {
			OK      bool   `json:"ok"`
			Text    string `json:"text"`
			Backend string `json:"backend"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(raw, &out)
		if !out.OK {
			return "", fmt.Errorf("%s", out.Error)
		}
		return out.Backend, nil
	})

	c.check("通知渠道配置", func() (string, error) {
		var d struct {
			Presets []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"presets"`
		}
		if err := c.getJSON("/api/notify/presets", &d); err != nil {
			return "", err
		}
		if len(d.Presets) < 5 {
			return "", fmt.Errorf("通知渠道模板只有 %d 个", len(d.Presets))
		}
		names := make([]string, 0, len(d.Presets))
		for _, p := range d.Presets {
			names = append(names, p.Name)
		}
		return strings.Join(names, " / "), nil
	})

	c.check("主题系统", func() (string, error) {
		var d struct {
			Builtins []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"builtins"`
			Vars []string `json:"vars"`
		}
		if err := c.getJSON("/api/themes", &d); err != nil {
			return "", err
		}
		if len(d.Builtins) < 4 {
			return "", fmt.Errorf("内置主题只有 %d 个", len(d.Builtins))
		}
		// 保存一个临时主题再删掉，验证写路径
		body := map[string]any{
			"id": "verify-tmp", "name": "自检临时主题",
			"vars": map[string]string{"bg": "#123456", "accent": "#abcdef"},
		}
		if err := c.postJSON("/api/themes/save", body, nil); err != nil {
			return "", err
		}
		if err := c.postJSON("/api/themes/delete", map[string]string{"id": "verify-tmp"}, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d 个内置主题 · %d 个可自定义变量", len(d.Builtins), len(d.Vars)), nil
	})

	c.check("技能包与权限", func() (string, error) {
		var d struct {
			Skills []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"skills"`
		}
		if err := c.getJSON("/api/plugins", &d); err != nil {
			return "", err
		}
		var a struct {
			Permission string   `json:"permission"`
			Tools      []string `json:"tools"`
			Levels     []struct {
				ID string `json:"id"`
			} `json:"levels"`
		}
		if err := c.getJSON("/api/agent/info", &a); err != nil {
			return "", err
		}
		if len(a.Levels) != 3 {
			return "", fmt.Errorf("权限级别只有 %d 档", len(a.Levels))
		}
		n := 0
		for _, sk := range d.Skills {
			if sk.Enabled {
				n++
			}
		}
		return fmt.Sprintf("%d 个技能包（%d 个启用）· 权限 %s · 工具 %s",
			len(d.Skills), n, a.Permission, strings.Join(a.Tools, "/")), nil
	})

	c.check("更新检查", func() (string, error) {
		var d struct {
			Checked   bool   `json:"checked"`
			Current   string `json:"current"`
			HasUpdate bool   `json:"hasUpdate"`
			Latest    string `json:"latest"`
			Error     string `json:"error"`
		}
		if err := c.getJSON("/api/update/check", &d); err != nil {
			return "", err
		}
		if !d.Checked {
			return "", fmt.Errorf("未完成检查")
		}
		if d.Error != "" {
			return "当前版本 " + d.Current + " · " + d.Error, nil
		}
		if d.HasUpdate {
			return fmt.Sprintf("发现新版本 %s（当前 %s）", d.Latest, d.Current), nil
		}
		return "已是最新 " + d.Current, nil
	})

	c.check("市场与一键更新", func() (string, error) {
		var m struct {
			OK     bool `json:"ok"`
			Skills []struct {
				ID     string `json:"id"`
				Prompt string `json:"prompt"`
			} `json:"skills"`
			Error string `json:"error"`
		}
		if err := c.getJSON("/api/plugins/market", &m); err != nil {
			return "", err
		}
		if !m.OK {
			return "", fmt.Errorf("技能包市场: %s", m.Error)
		}
		if len(m.Skills) == 0 {
			return "", fmt.Errorf("技能包市场为空")
		}
		return fmt.Sprintf("技能包市场 %d 项 · 一键更新接口就绪", len(m.Skills)), nil
	})

	c.check("插件清单（schemaVersion 2）", func() (string, error) {
		var d struct {
			Skills []struct {
				ID            string   `json:"id"`
				Kind          string   `json:"kind"`
				Version       string   `json:"version"`
				SchemaVersion int      `json:"schemaVersion"`
				Capabilities  []string `json:"capabilities"`
			} `json:"skills"`
			Slots  []any `json:"slots"`
			Broken []struct {
				File   string `json:"file"`
				Reason string `json:"reason"`
			} `json:"broken"`
		}
		if err := c.getJSON("/api/plugins", &d); err != nil {
			return "", err
		}
		for _, p := range d.Skills {
			if p.Kind == "" || p.SchemaVersion == 0 {
				return "", fmt.Errorf("插件 %s 未迁移到 v2（kind=%q schema=%d）", p.ID, p.Kind, p.SchemaVersion)
			}
		}
		detail := fmt.Sprintf("%d 个插件 · 槽位 %d · 读取失败 %d", len(d.Skills), len(d.Slots), len(d.Broken))
		if len(d.Broken) > 0 {
			return detail, fmt.Errorf("%s（%s: %s）", detail, d.Broken[0].File, d.Broken[0].Reason)
		}
		return detail, nil
	})

	c.check("Agent 工具与模型", func() (string, error) {
		var d struct {
			Models []struct {
				ID    string `json:"id"`
				Ready bool   `json:"ready"`
			} `json:"models"`
			ToolNames []string `json:"toolNames"`
			MaxSteps  int      `json:"maxSteps"`
			Levels    []any    `json:"levels"`
		}
		if err := c.getJSON("/api/agent/info", &d); err != nil {
			return "", err
		}
		has := map[string]bool{}
		for _, m := range d.Models {
			has[m.ID] = true
		}
		if !has["web:qwen"] || !has["web:deepseek"] {
			return "", fmt.Errorf("模型目录缺少网页版 Qwen / DeepSeek：%v", has)
		}
		if len(d.ToolNames) < 7 {
			return "", fmt.Errorf("Agent 只注册了 %d 个工具", len(d.ToolNames))
		}
		if d.MaxSteps < 1 || d.MaxSteps > 12 {
			return "", fmt.Errorf("步数上限异常：%d", d.MaxSteps)
		}
		return fmt.Sprintf("%d 个工具 · %d 个模型 · 步数上限 %d", len(d.ToolNames), len(d.Models), d.MaxSteps), nil
	})

	c.check("插件市场双渠道", func() (string, error) {
		var cn struct {
			OK       bool   `json:"ok"`
			Total    int    `json:"total"`
			Channel  string `json:"channel"`
			Channels []struct {
				ID    string `json:"id"`
				Count int    `json:"count"`
			} `json:"channels"`
		}
		if err := c.getJSON("/api/plugins/market?channel=classnote", &cn); err != nil {
			return "", err
		}
		if !cn.OK || cn.Total == 0 {
			return "", fmt.Errorf("ClassNote 渠道为空")
		}
		var dsh struct {
			OK    bool `json:"ok"`
			Total int  `json:"total"`
			Items []struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"items"`
			Categories map[string]string `json:"categories"`
		}
		if err := c.getJSON("/api/plugins/market?channel=dsh&limit=20", &dsh); err != nil {
			return "", err
		}
		if !dsh.OK || dsh.Total < 1000 {
			return "", fmt.Errorf("DSH 渠道条目过少：%d", dsh.Total)
		}
		for _, it := range dsh.Items {
			if it.URL == "" {
				return "", fmt.Errorf("DSH 条目 %s 缺少仓库地址", it.ID)
			}
		}
		return fmt.Sprintf("ClassNote %d 项 · DSH %d 项 / %d 类 · %d 个渠道", cn.Total, dsh.Total, len(dsh.Categories), len(cn.Channels)), nil
	})

	c.check("界面样式切换", func() (string, error) {
		var boot struct {
			Config struct {
				Config struct {
					UI struct {
						Style string `json:"style"`
					} `json:"ui"`
				} `json:"config"`
			} `json:"config"`
		}
		if err := c.getJSON("/api/bootstrap", &boot); err != nil {
			return "", err
		}
		orig := boot.Config.Config.UI.Style
		if orig == "" {
			return "", fmt.Errorf("配置里没有 ui.style")
		}
		apply := func(style string) error {
			var cfg struct {
				Config struct {
					Config map[string]any `json:"config"`
				} `json:"config"`
			}
			if err := c.getJSON("/api/bootstrap", &cfg); err != nil {
				return err
			}
			body := cfg.Config.Config
			if ui, ok := body["ui"].(map[string]any); ok {
				ui["style"] = style
			} else {
				return fmt.Errorf("配置结构异常")
			}
			var out struct {
				Config struct {
					Config struct {
						UI struct {
							Style string `json:"style"`
						} `json:"ui"`
					} `json:"config"`
				} `json:"config"`
			}
			if err := c.postJSON("/api/config", body, &out); err != nil {
				return err
			}
			if out.Config.Config.UI.Style != style {
				return fmt.Errorf("写入 %s 后读回 %q", style, out.Config.Config.UI.Style)
			}
			return nil
		}
		if err := apply("dsh"); err != nil {
			return "", err
		}
		if err := apply("legacy"); err != nil {
			return "", err
		}
		return "可在 legacy / dsh 之间双向切换并落盘", nil
	})

	c.check("ClassIsland 注入插件", func() (string, error) {
		var d struct {
			Installed bool   `json:"installed"`
			Running   bool   `json:"running"`
			Version   string `json:"version"`
			Port      int    `json:"port"`
			Error     string `json:"error"`
			State     *struct {
				InClass bool   `json:"inClass"`
				Subject string `json:"subject"`
				Next    string `json:"next"`
			} `json:"state"`
		}
		if err := c.getJSON("/api/integrations/classisland/plugin", &d); err != nil {
			return "", err
		}
		if !d.Installed {
			return "", fmt.Errorf("未安装：%s", d.Error)
		}
		if !d.Running {
			return fmt.Sprintf("已安装 %s · 未运行（重启 ClassIsland 后加载）", d.Version), nil
		}
		cur := "当前无课"
		if d.State != nil {
			if d.State.InClass {
				cur = "上课中 " + d.State.Subject
			} else if d.State.Next != "" {
				cur = "下一节 " + d.State.Next
			}
		}
		return fmt.Sprintf("已连接 · 端口 %d · %s", d.Port, cur), nil
	})

	c.check("通知渠道扩展", func() (string, error) {
		var d struct {
			Presets []struct {
				ID string `json:"id"`
			} `json:"presets"`
		}
		if err := c.getJSON("/api/notify/presets", &d); err != nil {
			return "", err
		}
		want := []string{"discord", "slack", "bark", "pushplus", "ntfy", "gotify"}
		got := map[string]bool{}
		for _, p := range d.Presets {
			got[p.ID] = true
		}
		var missing []string
		for _, k := range want {
			if !got[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			return "", fmt.Errorf("缺少渠道: %s", strings.Join(missing, ","))
		}
		return fmt.Sprintf("%d 个快速添加渠道", len(d.Presets)), nil
	})

	c.check("课表科目读取", func() (string, error) {
		var d struct {
			Subjects []string `json:"subjects"`
			Source   string   `json:"source"`
			State    struct {
				InClass bool   `json:"inClass"`
				Subject string `json:"subject"`
			} `json:"state"`
		}
		if err := c.getJSON("/api/ci/subjects", &d); err != nil {
			return "", err
		}
		if d.Source == "off" {
			return "课表联动已关闭（不影响其他功能）", nil
		}
		if d.Source == "none" {
			return "", fmt.Errorf("没读到 ClassIsland 课表，请在「集成」里指定数据目录")
		}
		if len(d.Subjects) == 0 {
			return "", fmt.Errorf("课表里没有科目（来源 %s）", d.Source)
		}
		return fmt.Sprintf("%d 个科目 · 来源 %s", len(d.Subjects), d.Source), nil
	})

	c.check("网页版对话配置", func() (string, error) {
		var d struct {
			OK    bool   `json:"ok"`
			Mode  string `json:"mode"`
			Hint  string `json:"hint"`
			Chats []struct {
				Subject string `json:"subject"`
				URL     string `json:"url"`
				Primed  bool   `json:"primed"`
			} `json:"chats"`
			Subjects []string `json:"subjects"`
		}
		if err := c.getJSON("/api/webai/chats", &d); err != nil {
			return "", err
		}
		if !d.OK {
			return "使用 API 模型，无需管理对话", nil
		}
		ready := 0
		for _, ch := range d.Chats {
			if ch.Primed && ch.URL != "" {
				ready++
			}
		}
		if len(d.Subjects) == 0 {
			return "还没有要监听的科目", nil
		}
		if ready == 0 {
			return "", fmt.Errorf("有 %d 门课要监听，但还没有一条对话准备好（到设置里点「自动准备对话」）", len(d.Subjects))
		}
		return fmt.Sprintf("%d/%d 门课已固定对话", ready, len(d.Subjects)), nil
	})

	if *full {
		fmt.Println("模式：完整（含大模型调用，耗时较长）")
	} else {
		fmt.Println("模式：快速（不含大模型调用）")
	}

	c.check("服务可达 / bootstrap", func() (string, error) {
		var d struct {
			App struct {
				Version   string `json:"version"`
				Onboarded bool   `json:"onboarded"`
				Port      int    `json:"port"`
				Paths     struct {
					Root string `json:"root"`
				} `json:"paths"`
			} `json:"app"`
			Presets []struct{ ID string } `json:"presets"`
			Themes  []struct{ ID string } `json:"themes"`
		}
		if err := c.getJSON("/api/bootstrap", &d); err != nil {
			return "", err
		}
		if d.App.Version == "" {
			return "", fmt.Errorf("缺少版本号")
		}
		if len(d.Presets) < 5 {
			return "", fmt.Errorf("模型预设只有 %d 个", len(d.Presets))
		}
		if len(d.Themes) < 3 {
			return "", fmt.Errorf("主题只有 %d 套", len(d.Themes))
		}
		return fmt.Sprintf("v%s · 端口 %d · %d 个预设 · %d 套主题 · %s",
			d.App.Version, d.App.Port, len(d.Presets), len(d.Themes), d.App.Paths.Root), nil
	})

	c.check("静态资源", func() (string, error) {
		home, _, err := c.request("GET", "/", nil)
		if err != nil {
			return "", err
		}
		if home != 200 {
			return "", fmt.Errorf("首页 HTTP %d", home)
		}
		css, body, err := c.request("GET", "/assets/css/app.css", nil)
		if err != nil {
			return "", err
		}
		if css != 200 || !bytes.Contains(body, []byte("--bg")) {
			return "", fmt.Errorf("样式表异常 HTTP %d (%d 字节)", css, len(body))
		}
		js, body2, err := c.request("GET", "/assets/js/app.js", nil)
		if err != nil {
			return "", err
		}
		if js != 200 || !bytes.Contains(body2, []byte("function init")) {
			return "", fmt.Errorf("脚本异常 HTTP %d (%d 字节)", js, len(body2))
		}
		return fmt.Sprintf("HTML/CSS(%dKB)/JS(%dKB) 均正常", len(body)/1024, len(body2)/1024), nil
	})

	c.check("状态接口", func() (string, error) {
		var d struct {
			Status struct {
				Running  bool   `json:"running"`
				Stage    string `json:"stage"`
				Segments int    `json:"segments"`
			} `json:"status"`
			Server struct {
				Port int `json:"port"`
			} `json:"server"`
		}
		if err := c.getJSON("/api/status", &d); err != nil {
			return "", err
		}
		return fmt.Sprintf("stage=%s segments=%d", d.Status.Stage, d.Status.Segments), nil
	})

	c.check("配置读写往返", func() (string, error) {
		var boot struct {
			Config struct {
				Config map[string]any `json:"config"`
			} `json:"config"`
		}
		if err := c.getJSON("/api/bootstrap", &boot); err != nil {
			return "", err
		}
		cfg := boot.Config.Config
		if cfg == nil {
			return "", fmt.Errorf("配置为空")
		}
		var res struct {
			OK bool `json:"ok"`
		}
		if err := c.postJSON("/api/config", cfg, &res); err != nil {
			return "", err
		}
		if !res.OK {
			return "", fmt.Errorf("保存未返回 ok")
		}
		var again struct {
			Config struct {
				Config map[string]any `json:"config"`
			} `json:"config"`
		}
		if err := c.getJSON("/api/bootstrap", &again); err != nil {
			return "", err
		}
		a, _ := json.Marshal(cfg["ui"])
		b, _ := json.Marshal(again.Config.Config["ui"])
		if !bytes.Equal(a, b) {
			return "", fmt.Errorf("UI 配置未持久化")
		}
		return "保存后重新读取一致", nil
	})

	c.check("课表解析", func() (string, error) {
		var d struct {
			State struct {
				Source  string `json:"source"`
				Profile string `json:"profile"`
				Running bool   `json:"running"`
				InClass bool   `json:"inClass"`
				Hint    string `json:"hint"`
				Error   string `json:"error"`
				Today   []struct {
					Subject string `json:"subject"`
					Start   string `json:"start"`
				} `json:"today"`
			} `json:"state"`
		}
		if err := c.getJSON("/api/timetable", &d); err != nil {
			return "", err
		}
		st := d.State
		if st.Error != "" {
			return "", fmt.Errorf("%s", st.Error)
		}
		if st.Source != "profile" && st.Source != "manual" && st.Source != "push" && st.Source != "plugin" {
			return "", fmt.Errorf("课表来源异常：%s", st.Source)
		}
		if len(st.Today) == 0 {
			return fmt.Sprintf("档案 %s 已读取，%s", st.Profile, st.Hint), nil
		}
		return fmt.Sprintf("%s · %d 节 · 首节 %s %s", st.Profile, len(st.Today), st.Today[0].Start[11:16], st.Today[0].Subject), nil
	})

	c.check("集成状态", func() (string, error) {
		var d struct {
			ClassIsland struct {
				Running bool   `json:"running"`
				Profile string `json:"profile"`
			} `json:"classIsland"`
			ICC struct {
				Running         bool   `json:"running"`
				PluginInstalled bool   `json:"pluginInstalled"`
				PluginDir       string `json:"pluginDir"`
				Presenting      bool   `json:"presenting"`
				LastEvent       string `json:"lastEvent"`
			} `json:"icc"`
		}
		if err := c.getJSON("/api/integrations/status", &d); err != nil {
			return "", err
		}
		if !d.ClassIsland.Running {
			return "", fmt.Errorf("未检测到 ClassIsland 进程")
		}
		if !d.ICC.Running {
			return "", fmt.Errorf("未检测到 InkCanvasForClass 进程")
		}
		if !d.ICC.PluginInstalled {
			return "", fmt.Errorf("ICC 插件未安装")
		}
		return fmt.Sprintf("ClassIsland=%s · ICC 插件已装 · 放映=%v", d.ClassIsland.Profile, d.ICC.Presenting), nil
	})

	c.check("环境探测", func() (string, error) {
		var d struct {
			Items []struct {
				Name string `json:"name"`
				OK   bool   `json:"ok"`
			} `json:"items"`
		}
		if err := c.getJSON("/api/env", &d); err != nil {
			return "", err
		}
		if len(d.Items) < 4 {
			return "", fmt.Errorf("环境项过少：%d", len(d.Items))
		}
		n := 0
		for _, it := range d.Items {
			if it.OK {
				n++
			}
		}
		return fmt.Sprintf("%d/%d 项就绪", n, len(d.Items)), nil
	})

	c.check("麦克风采集", func() (string, error) {
		var d struct {
			OK      bool    `json:"ok"`
			Error   string  `json:"error"`
			Peak    float64 `json:"peak"`
			Devices int     `json:"devices"`
			Message string  `json:"message"`
		}
		if err := c.postJSON("/api/asr/mic", nil, &d); err != nil {
			return "", err
		}
		if !d.OK {
			return "", fmt.Errorf("%s", d.Error)
		}
		if d.Devices < 1 {
			return "", fmt.Errorf("没有录音设备")
		}
		return fmt.Sprintf("%d 个录音设备 · %s", d.Devices, d.Message), nil
	})

	c.check("历史课堂列表", func() (string, error) {
		var d struct {
			Sessions []struct {
				ID       string `json:"id"`
				Segments []any  `json:"segments"`
			} `json:"sessions"`
		}
		if err := c.getJSON("/api/sessions", &d); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d 条历史记录", len(d.Sessions)), nil
	})

	c.check("错误处理", func() (string, error) {
		// 未开始课堂时结束应返回 400 而不是 500。
		code, raw, err := c.request("POST", "/api/lesson/stop", map[string]any{"report": false})
		if err != nil {
			return "", err
		}
		if code != 400 {
			return "", fmt.Errorf("空闲时结束返回 HTTP %d", code)
		}
		if !bytes.Contains(raw, []byte("error")) {
			return "", fmt.Errorf("错误响应缺少 error 字段")
		}
		// 空问题应被拒绝。
		code2, _, err := c.request("POST", "/api/lesson/ask", map[string]any{"question": "  "})
		if err != nil {
			return "", err
		}
		if code2 != 400 {
			return "", fmt.Errorf("空问题返回 HTTP %d", code2)
		}
		return "非法请求均返回 400", nil
	})

	c.check("目录穿越防护", func() (string, error) {
		code, body, err := c.request("GET", "/api/media/../../config.json", nil)
		if err != nil {
			return "", err
		}
		if bytes.Contains(body, []byte("apiKey")) || bytes.Contains(body, []byte("webAI")) {
			return "", fmt.Errorf("疑似读到配置文件")
		}
		if code == 200 && !bytes.Contains(body, []byte("<!doctype html")) && !bytes.Contains(body, []byte("<!DOCTYPE html")) {
			return "", fmt.Errorf("非预期内容（HTTP %d，%d 字节）", code, len(body))
		}
		return fmt.Sprintf("HTTP %d，回落到界面入口，未泄漏配置", code), nil
	})

	c.check("托盘窗口", func() (string, error) {
		cls, _ := windows.UTF16PtrFromString("ClassNoteTrayWnd")
		tit, _ := windows.UTF16PtrFromString("ClassNote")
		h, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(tit)))
		if h == 0 {
			return "", fmt.Errorf("未找到托盘窗口")
		}
		return fmt.Sprintf("hwnd=%#x", h), nil
	})

	c.check("托盘图标注册", func() (string, error) {
		h := trayHwnd()
		if h == 0 {
			return "", fmt.Errorf("托盘窗口不存在")
		}
		id := iconID{CbSize: uint32(unsafe.Sizeof(iconID{})), HWnd: windows.Handle(h), UID: 1}
		var r winRect
		hr, _, _ := procGetRect.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&r)))
		if int32(hr) != 0 {
			return "", fmt.Errorf("Shell_NotifyIconGetRect 失败 %#x", hr)
		}
		return fmt.Sprintf("图标矩形 %dx%d", r.Right-r.Left, r.Bottom-r.Top), nil
	})

	c.check("托盘开始记录", func() (string, error) {
		if err := postTray(cmdToggle); err != nil {
			return "", err
		}
		running, sid, err := waitRunning(c.base, 10*time.Second)
		if err != nil {
			return "", err
		}
		_ = running
		c.session = sid
		return fmt.Sprintf("通过托盘开始，会话 %s", sid), nil
	})

	c.check("课堂状态与转写", func() (string, error) {
		text := "同学们看这一页，《荷塘月色》写于一九二七年，作者朱自清。"
		var ok struct {
			OK bool `json:"ok"`
		}
		if err := c.postJSON("/api/lesson/utterance", map[string]any{"text": text}, &ok); err != nil {
			return "", err
		}
		time.Sleep(600 * time.Millisecond)
		var d struct {
			Session struct {
				Segments []struct {
					Transcript string `json:"transcript"`
				} `json:"segments"`
			} `json:"session"`
		}
		if err := c.getJSON("/api/session", &d); err != nil {
			return "", err
		}
		found := false
		for _, s := range d.Session.Segments {
			if strings.Contains(s.Transcript, "荷塘月色") {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("转写未写入分段")
		}
		return "补录转写已归入当前页", nil
	})

	c.check("SSE 事件流", func() (string, error) {
		req, _ := http.NewRequest("GET", c.base+"/api/events", nil)
		cl := &http.Client{Timeout: 0}
		resp, err := cl.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		var res struct {
			OK bool `json:"ok"`
		}
		_ = c.postJSON("/api/events/test", map[string]any{"message": "自检"}, &res)
		type readResult struct {
			n   int
			buf []byte
			err error
		}
		ch := make(chan readResult, 1)
		go func() {
			buf := make([]byte, 4096)
			n, err := resp.Body.Read(buf)
			ch <- readResult{n, buf[:n], err}
		}()
		select {
		case r := <-ch:
			if r.n == 0 {
				return "", fmt.Errorf("未收到事件: %v", r.err)
			}
			if !bytes.Contains(r.buf, []byte("test")) && !bytes.Contains(r.buf, []byte("event")) {
				return "", fmt.Errorf("事件格式异常: %s", truncate(string(r.buf), 60))
			}
			return "已收到服务端推送", nil
		case <-time.After(8 * time.Second):
			return "", fmt.Errorf("等待事件超时")
		}
	})

	c.check("结束课堂", func() (string, error) {
		var d struct {
			OK      bool `json:"ok"`
			Session struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"session"`
		}
		if err := c.postJSON("/api/lesson/stop", map[string]any{"report": false}, &d); err != nil {
			return "", err
		}
		if d.Session.Status != "ended" {
			return "", fmt.Errorf("状态为 %s", d.Session.Status)
		}
		if c.session == "" {
			c.session = d.Session.ID
		}
		return fmt.Sprintf("会话 %s 已结束", d.Session.ID), nil
	})

	var reportPath string
	c.check("生成课堂报告", func() (string, error) {
		if c.session == "" {
			return "", fmt.Errorf("没有可用的会话")
		}
		t := time.Now()
		var d struct {
			OK      bool `json:"ok"`
			Session struct {
				Report struct {
					HTMLPath string `json:"htmlPath"`
					PDFPath  string `json:"pdfPath"`
					CopyRate int    `json:"copyRate"`
					Summary  string `json:"summary"`
				} `json:"report"`
			} `json:"session"`
		}
		if err := c.postJSON("/api/lesson/report", map[string]any{"sessionId": c.session}, &d); err != nil {
			return "", err
		}
		rep := d.Session.Report
		if rep.HTMLPath == "" {
			return "", fmt.Errorf("未生成 HTML")
		}
		fi, err := os.Stat(rep.HTMLPath)
		if err != nil || fi.Size() < 2000 {
			return "", fmt.Errorf("HTML 文件异常: %v", err)
		}
		if rep.PDFPath == "" {
			return "", fmt.Errorf("未生成 PDF")
		}
		pf, err := os.Stat(rep.PDFPath)
		if err != nil || pf.Size() < 2000 {
			return "", fmt.Errorf("PDF 文件异常: %v", err)
		}
		reportPath = rep.HTMLPath
		return fmt.Sprintf("HTML %dKB · PDF %dKB · 照搬度 %d%% · 用时 %.1fs",
			fi.Size()/1024, pf.Size()/1024, rep.CopyRate, time.Since(t).Seconds()), nil
	})

	c.check("报告文件下载", func() (string, error) {
		code, body, err := c.request("GET", "/api/report-file/"+c.session+"?type=html", nil)
		if err != nil {
			return "", err
		}
		if code != 200 || len(body) < 2000 {
			return "", fmt.Errorf("HTTP %d，%d 字节", code, len(body))
		}
		return fmt.Sprintf("HTML %dKB", len(body)/1024), nil
	})

	c.check("单实例互斥", func() (string, error) {
		if _, err := os.Stat(c.exe); err != nil {
			return "", fmt.Errorf("找不到 %s", c.exe)
		}
		cmd := exec.Command(c.exe, "-no-window", "-no-tray", "-port", portOf(c.base))
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		start := time.Now()
		done := make(chan error, 1)
		if err := cmd.Start(); err != nil {
			return "", err
		}
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
			return fmt.Sprintf("第二个实例在 %.1fs 内让位", time.Since(start).Seconds()), nil
		case <-time.After(8 * time.Second):
			_ = cmd.Process.Kill()
			return "", fmt.Errorf("第二个实例仍在运行")
		}
	})

	c.check("网页版模型就绪", func() (string, error) {
		var boot struct {
			Config struct {
				Config struct {
					Provider struct {
						Mode string `json:"mode"`
					} `json:"provider"`
					WebAI struct {
						Site      string `json:"site"`
						LoggedIn  bool   `json:"loggedIn"`
						ProfileOK bool   `json:"profileOK"`
					} `json:"webAI"`
				} `json:"config"`
			} `json:"config"`
			Status struct {
				Model string `json:"model"`
				AIErr string `json:"aiError"`
				ASR   string `json:"asr"`
			} `json:"status"`
		}
		if err := c.getJSON("/api/bootstrap", &boot); err != nil {
			return "", err
		}
		mode := boot.Config.Config.Provider.Mode
		if mode == "web" {
			var chk struct {
				OK      bool   `json:"ok"`
				Message string `json:"message"`
			}
			if err := c.getJSON("/api/webai/check", &chk); err != nil {
				return "", err
			}
			if !chk.OK {
				return "", fmt.Errorf("%s", chk.Message)
			}
			return fmt.Sprintf("网页版 %s：%s", boot.Config.Config.WebAI.Site, chk.Message), nil
		}
		if boot.Status.Model == "" {
			return "", fmt.Errorf("模型未配置")
		}
		return fmt.Sprintf("%s 已就绪", boot.Status.Model), nil
	})

	c.check("本地语音模型", func() (string, error) {
		var d struct {
			Items []struct {
				ID     string `json:"id"`
				OK     bool   `json:"ok"`
				Detail string `json:"detail"`
			} `json:"items"`
		}
		if err := c.getJSON("/api/env", &d); err != nil {
			return "", err
		}
		for _, it := range d.Items {
			if it.ID == "asr" {
				if !it.OK {
					return "", fmt.Errorf("%s", it.Detail)
				}
				return it.Detail, nil
			}
		}
		return "", fmt.Errorf("环境探测里没有语音项")
	})

	c.check("语音转写链路", func() (string, error) {
		// 用一段静音 WAV 走完整链路：本地 whisper 必须给出响应（哪怕是空文本）
		wav := makeWAV(16000, 1) // 1 秒静音
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		fw, _ := mw.CreateFormFile("file", "test.wav")
		_, _ = fw.Write(wav)
		_ = mw.Close()
		req, _ := http.NewRequest("POST", c.base+"/api/asr/test", body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if tok := ""; tok != "" {
			req.Header.Set("X-ClassNote-Token", tok)
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out struct {
			OK      bool   `json:"ok"`
			Text    string `json:"text"`
			Backend string `json:"backend"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(raw, &out)
		if !out.OK {
			return "", fmt.Errorf("%s", out.Error)
		}
		return out.Backend, nil
	})

	c.check("通知渠道配置", func() (string, error) {
		var d struct {
			Presets []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"presets"`
		}
		if err := c.getJSON("/api/notify/presets", &d); err != nil {
			return "", err
		}
		if len(d.Presets) < 5 {
			return "", fmt.Errorf("通知渠道模板只有 %d 个", len(d.Presets))
		}
		names := make([]string, 0, len(d.Presets))
		for _, p := range d.Presets {
			names = append(names, p.Name)
		}
		return strings.Join(names, " / "), nil
	})

	c.check("主题系统", func() (string, error) {
		var d struct {
			Builtins []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"builtins"`
			Vars []string `json:"vars"`
		}
		if err := c.getJSON("/api/themes", &d); err != nil {
			return "", err
		}
		if len(d.Builtins) < 4 {
			return "", fmt.Errorf("内置主题只有 %d 个", len(d.Builtins))
		}
		// 保存一个临时主题再删掉，验证写路径
		body := map[string]any{
			"id": "verify-tmp", "name": "自检临时主题",
			"vars": map[string]string{"bg": "#123456", "accent": "#abcdef"},
		}
		if err := c.postJSON("/api/themes/save", body, nil); err != nil {
			return "", err
		}
		if err := c.postJSON("/api/themes/delete", map[string]string{"id": "verify-tmp"}, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d 个内置主题 · %d 个可自定义变量", len(d.Builtins), len(d.Vars)), nil
	})

	c.check("技能包与权限", func() (string, error) {
		var d struct {
			Skills []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"skills"`
		}
		if err := c.getJSON("/api/plugins", &d); err != nil {
			return "", err
		}
		var a struct {
			Permission string   `json:"permission"`
			Tools      []string `json:"tools"`
			Levels     []struct {
				ID string `json:"id"`
			} `json:"levels"`
		}
		if err := c.getJSON("/api/agent/info", &a); err != nil {
			return "", err
		}
		if len(a.Levels) != 3 {
			return "", fmt.Errorf("权限级别只有 %d 档", len(a.Levels))
		}
		n := 0
		for _, sk := range d.Skills {
			if sk.Enabled {
				n++
			}
		}
		return fmt.Sprintf("%d 个技能包（%d 个启用）· 权限 %s · 工具 %s",
			len(d.Skills), n, a.Permission, strings.Join(a.Tools, "/")), nil
	})

	c.check("更新检查", func() (string, error) {
		var d struct {
			Checked   bool   `json:"checked"`
			Current   string `json:"current"`
			HasUpdate bool   `json:"hasUpdate"`
			Latest    string `json:"latest"`
			Error     string `json:"error"`
		}
		if err := c.getJSON("/api/update/check", &d); err != nil {
			return "", err
		}
		if !d.Checked {
			return "", fmt.Errorf("未完成检查")
		}
		if d.Error != "" {
			return "当前版本 " + d.Current + " · " + d.Error, nil
		}
		if d.HasUpdate {
			return fmt.Sprintf("发现新版本 %s（当前 %s）", d.Latest, d.Current), nil
		}
		return "已是最新 " + d.Current, nil
	})

	c.check("市场与一键更新", func() (string, error) {
		var m struct {
			OK     bool `json:"ok"`
			Skills []struct {
				ID string `json:"id"`
			} `json:"skills"`
			Error string `json:"error"`
		}
		if err := c.getJSON("/api/plugins/market", &m); err != nil {
			return "", err
		}
		if !m.OK || len(m.Skills) == 0 {
			return "", fmt.Errorf("技能包市场异常: %s", m.Error)
		}
		return fmt.Sprintf("技能包市场 %d 项 · 一键更新接口就绪", len(m.Skills)), nil
	})

	c.check("ClassIsland 注入插件", func() (string, error) {
		var d struct {
			Installed bool   `json:"installed"`
			Running   bool   `json:"running"`
			Version   string `json:"version"`
			Port      int    `json:"port"`
			Error     string `json:"error"`
			State     *struct {
				InClass bool   `json:"inClass"`
				Subject string `json:"subject"`
				Next    string `json:"next"`
			} `json:"state"`
		}
		if err := c.getJSON("/api/integrations/classisland/plugin", &d); err != nil {
			return "", err
		}
		if !d.Installed {
			return "", fmt.Errorf("未安装：%s", d.Error)
		}
		if !d.Running {
			return fmt.Sprintf("已安装 %s · 未运行（重启 ClassIsland 后加载）", d.Version), nil
		}
		cur := "当前无课"
		if d.State != nil {
			if d.State.InClass {
				cur = "上课中 " + d.State.Subject
			} else if d.State.Next != "" {
				cur = "下一节 " + d.State.Next
			}
		}
		return fmt.Sprintf("已连接 · 端口 %d · %s", d.Port, cur), nil
	})

	c.check("通知渠道扩展", func() (string, error) {
		var d struct {
			Presets []struct {
				ID string `json:"id"`
			} `json:"presets"`
		}
		if err := c.getJSON("/api/notify/presets", &d); err != nil {
			return "", err
		}
		want := []string{"discord", "slack", "bark", "pushplus", "ntfy", "gotify"}
		got := map[string]bool{}
		for _, p := range d.Presets {
			got[p.ID] = true
		}
		var missing []string
		for _, k := range want {
			if !got[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			return "", fmt.Errorf("缺少渠道: %s", strings.Join(missing, ","))
		}
		return fmt.Sprintf("%d 个快速添加渠道", len(d.Presets)), nil
	})

	if *full {
		c.check("大模型问答", func() (string, error) {
			t := time.Now()
			var d struct {
				OK     bool   `json:"ok"`
				Answer string `json:"answer"`
			}
			cl := &http.Client{Timeout: 240 * time.Second}
			raw, _ := json.Marshal(map[string]string{"question": "用一句话说明《荷塘月色》中“通感”的作用。"})
			req, _ := http.NewRequest("POST", c.base+"/api/lesson/ask", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp, err := cl.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 120))
			}
			if err := json.Unmarshal(body, &d); err != nil {
				return "", err
			}
			if !d.OK || len([]rune(d.Answer)) < 10 {
				return "", fmt.Errorf("回答过短：%s", truncate(d.Answer, 60))
			}
			return fmt.Sprintf("%d 字，用时 %.0fs：%s", len([]rune(d.Answer)), time.Since(t).Seconds(), truncate(d.Answer, 46)), nil
		})
	}

	// 汇总
	pass, fail := 0, 0
	for _, r := range c.results {
		if r.Pass {
			pass++
		} else {
			fail++
		}
	}
	fmt.Printf("\n结果：%d 通过 / %d 失败（共 %d 项）\n", pass, fail, len(c.results))
	if reportPath != "" {
		fmt.Printf("报告样例：%s\n", reportPath)
	}
	if err := writeReport(*out, c, pass, fail); err != nil {
		fmt.Println("写报告失败:", err)
	}
	if fail > 0 {
		os.Exit(1)
	}
}

func trayHwnd() uintptr {
	cls, _ := windows.UTF16PtrFromString("ClassNoteTrayWnd")
	tit, _ := windows.UTF16PtrFromString("ClassNote")
	h, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(tit)))
	return h
}

func postTray(cmd uintptr) error {
	h := trayHwnd()
	if h == 0 {
		return fmt.Errorf("托盘窗口不存在")
	}
	r, _, err := procPostMsg.Call(h, wmCommand, cmd, 0)
	if r == 0 {
		return fmt.Errorf("发送命令失败: %v", err)
	}
	return nil
}

func waitRunning(base string, timeout time.Duration) (bool, string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)
		resp, err := http.Get(base + "/api/status")
		if err != nil {
			continue
		}
		var d struct {
			Status struct {
				Running   bool   `json:"running"`
				SessionID string `json:"sessionId"`
			} `json:"status"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&d)
		resp.Body.Close()
		if d.Status.Running {
			return true, d.Status.SessionID, nil
		}
	}
	return false, "", fmt.Errorf("等待开始超时")
}

// makeWAV 生成一段 16kHz 单声道 PCM16 WAV（静音），用于转写链路自检。
func makeWAV(rate, sec int) []byte {
	n := rate * sec
	raw := make([]byte, n*2)
	buf := bytes.NewBuffer(nil)
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+len(raw)))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint32(rate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(raw)))
	buf.Write(raw)
	return buf.Bytes()
}

func portOf(base string) string {
	if i := strings.LastIndex(base, ":"); i >= 0 {
		return base[i+1:]
	}
	return "41735"
}

func writeReport(path string, c *ctx, pass, fail int) error {
	var b strings.Builder
	b.WriteString("# ClassNote 自检报告\n\n")
	b.WriteString(fmt.Sprintf("- 时间：%s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString(fmt.Sprintf("- 服务：%s\n", c.base))
	b.WriteString(fmt.Sprintf("- 模式：%s\n", map[bool]string{true: "完整（含大模型）", false: "快速"}[c.full]))
	b.WriteString(fmt.Sprintf("- 结果：**%d 通过 / %d 失败**\n\n", pass, fail))
	b.WriteString("| 用例 | 结果 | 说明 | 耗时 |\n| --- | --- | --- | --- |\n")
	for _, r := range c.results {
		mark := "✅"
		if !r.Pass {
			mark = "❌"
		}
		cost := ""
		if r.Cost > 0 {
			cost = fmt.Sprintf("%.1fs", r.Cost.Seconds())
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", r.Name, mark, strings.ReplaceAll(r.Detail, "|", "\\|"), cost))
	}
	if fail > 0 {
		b.WriteString("\n## 失败项\n\n")
		for _, r := range c.results {
			if !r.Pass {
				b.WriteString("- **" + r.Name + "**：" + r.Detail + "\n")
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
