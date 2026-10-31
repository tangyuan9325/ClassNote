// dsprobe 探测 DeepSeek 网页版能否访问与登录态
package main

import (
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"classnote/internal/llm"
)

func main() {
	proc, err := llm.LaunchBrowser(llm.BrowserBinary(), `E:/私人/01_项目/ClassNote/build/data/dsprofile`, true)
	if err != nil {
		fmt.Println("launch:", err)
		return
	}
	defer func() {
		if proc.Cmd != nil && proc.Cmd.Process != nil {
			proc.Cmd.Process.Kill()
		}
	}()
	br := rod.New().ControlURL(proc.WS)
	if err := br.Connect(); err != nil {
		fmt.Println("connect:", err)
		return
	}
	defer br.Close()
	page, err := br.Page(proto.TargetCreateTarget{URL: "https://chat.deepseek.com/"})
	if err != nil {
		fmt.Println("page:", err)
		return
	}
	_ = page.Timeout(40 * time.Second).WaitLoad()
	time.Sleep(4 * time.Second)
	info, _ := page.Info()
	fmt.Println("url:", info.URL, "| title:", info.Title)
	txt, _ := page.Eval(`() => document.body ? document.body.innerText.slice(0, 300) : ''`)
	fmt.Println("body:", txt.Value.Str())
	res, _ := page.Eval(`() => JSON.stringify({
		textarea: document.querySelectorAll('textarea').length,
		input: document.querySelectorAll('input').length,
		buttons: [...document.querySelectorAll('button')].map(b => (b.innerText || b.getAttribute('aria-label') || '').trim()).filter(Boolean).slice(0, 10),
		markdown: document.querySelectorAll('[class*=markdown]').length
	})`)
	fmt.Println("dom:", res.Value.Str())
}
