// Package agent 实现 ClassNote 的 Agent 循环。
//
// 设计参照 DeepSeek Harness（github.com/deepseek-ai/deepseek-harness，
// MIT License）的 turn / step / tool 流水线：
//
//	turn  = 一次用户请求
//	step  = 一轮「模型思考 → 选择动作 → 执行工具 → 观察结果」
//
// 与 DSH 的差别：DSH 的工具走各家的原生 function calling；ClassNote 还要
// 支持网页版 Qwen / DeepSeek（它们没有 function calling），所以这里统一用
// **JSON 动作协议**——模型每步只输出一个 JSON，宿主负责解析、鉴权、执行。
// 这样 API 模型和网页版模型跑的是同一条链路，行为可预期。
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"classnote/internal/config"
	"classnote/internal/llm"
	"classnote/internal/logx"
)

// 事件类型。前端按这些名字渲染时间线。
const (
	EvStart      = "agent_start"
	EvStep       = "agent_step"
	EvTool       = "agent_tool"
	EvToolResult = "agent_tool_result"
	EvApproval   = "agent_approval"
	EvFinal      = "agent_final"
	EvError      = "agent_error"
)

// ErrStopped 表示 Agent 主动停止（步数用尽 / 无进展 / 用户取消）。
var ErrStopped = errors.New("agent 已停止")

// ToolSpec 描述一个工具。Schema 是一段人话 + 参数说明，直接拼进提示词。
type ToolSpec struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Args        []Arg  `json:"args,omitempty"`
	ReadOnly    bool   `json:"readOnly"`
	Permission  string `json:"permission"`
	// Subject 限定工具只在某类课堂可用（空 = 通用）。
	Subject string `json:"subject,omitempty"`
}

// Arg 描述一个参数。
type Arg struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // string | int | bool
	Required bool   `json:"required,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Tool 是可执行的本地能力。
type Tool interface {
	Spec() ToolSpec
	Run(ctx context.Context, args map[string]any) (ToolResult, error)
}

// ToolResult 是工具的输出。
type ToolResult struct {
	// Content 是回灌给模型看的观察结果，必须是纯文本。
	Content string `json:"content"`
	// Data 给界面用（结构化），可为 nil。
	Data any `json:"data,omitempty"`
	// Err 非空表示工具执行失败，仍然作为观察结果回灌（让模型自己纠错）。
	Err string `json:"err,omitempty"`
}

// Step 是一次模型-工具往返的记录。
type Step struct {
	Index     int    `json:"index"`
	Thought   string `json:"thought,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Args      string `json:"args,omitempty"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	Duration  int64  `json:"durationMs,omitempty"`
	Approved  bool   `json:"approved,omitempty"`
	Skipped   bool   `json:"skipped,omitempty"`
	RawOutput string `json:"-"`
}

// ApprovalRequest 是一次人工审批请求。
type ApprovalRequest struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Title   string `json:"title"`
	Args    string `json:"args"`
	Preview string `json:"preview"`
}

// ApprovalFunc 返回是否放行。ctx 取消时应当返回 false。
type ApprovalFunc func(ctx context.Context, req ApprovalRequest) (bool, error)

// Request 是一次 Agent 运行请求。
type Request struct {
	Question  string
	SessionID string
	History   []llm.Message
	Subject   string
	// MaxSteps 为 0 时用配置里的值。
	MaxSteps int
}

// RunResult 是一次运行的最终结果。
type RunResult struct {
	Answer    string    `json:"answer"`
	Steps     []Step    `json:"steps"`
	Model     string    `json:"model"`
	Stop      string    `json:"stop,omitempty"`
	ElapsedMS int64     `json:"elapsedMs"`
	Usage     llm.Usage `json:"usage"`
}

// Runner 执行 Agent 循环。
type Runner struct {
	client  llm.Client
	tools   *Toolset
	cfg     config.Config
	plugins PluginPrompt
	emit    func(typ string, data any)
	approve ApprovalFunc
	mu      sync.Mutex
}

// PluginPrompt 由插件仓库提供（避免 agent 直接依赖 plugin 包）。
type PluginPrompt interface {
	Prompt() string
	Tools() []string
}

// New 创建 Runner。emit 可为 nil。
func New(client llm.Client, tools *Toolset, cfg config.Config, plugins PluginPrompt, emit func(string, any)) *Runner {
	return &Runner{client: client, tools: tools, cfg: cfg, plugins: plugins, emit: emit}
}

// SetApproval 设置审批回调；为 nil 时按权限级别自动决定。
func (r *Runner) SetApproval(fn ApprovalFunc) { r.approve = fn }

func (r *Runner) fire(typ string, data any) {
	if r.emit != nil {
		r.emit(typ, data)
	}
}

const systemPromptTemplate = `你是 ClassNote（课记）的课堂 Agent，服务对象是正在上课的中学老师。
你能调用本机工具读取这节课的幻灯片、转写与笔记，也能改写笔记、生成报告。

# 工作方式
每一步你只能输出一个 JSON 对象，不要输出别的文字，不要用代码块包裹。

需要调用工具时：
{"thought":"一句话说明你要做什么","tool":"工具名","args":{"参数":"值"}}

可以给出最终回答时：
{"thought":"一句话说明结论","final":"给老师看的回答（可用 Markdown）"}

# 规则
1. 先看清材料再下结论。材料里没有的信息写「这堂课没有记录到」，绝不编造。
2. 一次只调一个工具；拿到结果后再决定下一步。
3. 工具报错就换参数或换工具，不要重复同样的调用。
4. 最多 %d 步。步数用尽前必须给出 final。
5. 回答用简体中文，直接给结论和可执行的动作，不要寒暄，不要复述问题。
6. 改笔记、生成报告属于写操作，会请老师确认；被拒绝就说明情况并给替代方案。

# 可用工具
%s
%s`

// Run 执行一次完整 turn。
func (r *Runner) Run(ctx context.Context, req Request) (*RunResult, error) {
	if r.client == nil {
		return nil, llm.ErrNotConfigured
	}
	start := time.Now()
	maxSteps := req.MaxSteps
	if maxSteps <= 0 {
		maxSteps = r.cfg.Agent.MaxSteps
	}
	if maxSteps <= 0 || maxSteps > 12 {
		maxSteps = 6
	}
	allowed := r.allowedTools()
	catalog := catalogText(r.tools.List(allowed))
	sys := fmt.Sprintf(systemPromptTemplate, maxSteps, catalog, r.promptExtra())
	if req.Subject != "" {
		sys += "\n# 当前课堂\n学科：" + req.Subject + "\n"
	}

	msgs := []llm.Message{llm.TextMessage(llm.RoleSystem, sys)}
	hist := req.History
	if len(hist) > 8 {
		hist = hist[len(hist)-8:]
	}
	for _, h := range hist {
		if strings.TrimSpace(h.Text()) == "" {
			continue
		}
		msgs = append(msgs, h)
	}
	msgs = append(msgs, llm.UserText(req.Question))

	// 工具只能看这一堂课；模型自己传 sessionId 是无效的。
	ctx = WithSession(ctx, req.SessionID)

	res := &RunResult{Model: describe(r.client)}
	r.fire(EvStart, map[string]any{
		"question": req.Question, "model": res.Model, "maxSteps": maxSteps,
		"tools": allowed, "sessionId": req.SessionID,
	})

	seen := map[string]int{}
	lastSig := ""
	repeat := 0
	for i := 1; i <= maxSteps; i++ {
		if err := ctx.Err(); err != nil {
			res.Stop = "cancelled"
			res.ElapsedMS = time.Since(start).Milliseconds()
			return res, ErrStopped
		}
		r.fire(EvStep, map[string]any{"index": i, "max": maxSteps})
		out, err := r.client.Chat(ctx, msgs, llm.Options{
			Temperature: 0.2,
			MaxTokens:   r.cfg.Provider.MaxTokens,
			Subject:     "课堂Agent",
			FollowUp:    i > 1,
		})
		if err != nil {
			res.Stop = "llm-error"
			res.ElapsedMS = time.Since(start).Milliseconds()
			r.fire(EvError, map[string]any{"message": err.Error(), "step": i})
			if res.Answer == "" {
				return res, err
			}
			return res, nil
		}
		if out != nil {
			res.Usage.TotalTokens += out.Usage.TotalTokens
			res.Usage.PromptTokens += out.Usage.PromptTokens
			res.Usage.CompletionTokens += out.Usage.CompletionTokens
		}
		text := ""
		if out != nil {
			text = out.Text
		}
		act := parseAction(text)
		step := Step{Index: i, Thought: act.Thought, RawOutput: text}
		if act.Final != "" || act.Tool == "" {
			answer := act.Final
			if answer == "" {
				answer = strings.TrimSpace(text)
			}
			res.Answer = answer
			res.Stop = "final"
			res.ElapsedMS = time.Since(start).Milliseconds()
			r.fire(EvFinal, map[string]any{"answer": answer, "steps": i})
			return res, nil
		}

		// 模型想调工具。
		toolName := act.Tool
		if !contains(allowed, toolName) {
			obs := fmt.Sprintf("工具 %q 不可用。可用工具：%s", toolName, strings.Join(allowed, "、"))
			step.Error = obs
			msgs = append(msgs,
				llm.TextMessage(llm.RoleAssistant, text),
				llm.UserText("【工具结果】\n"+obs))
			res.Steps = append(res.Steps, step)
			r.fire(EvToolResult, map[string]any{"index": i, "tool": toolName, "error": obs})
			continue
		}

		sig := sign(toolName, act.Args)
		seen[sig]++
		if sig == lastSig {
			repeat++
		} else {
			repeat = 0
		}
		lastSig = sig
		if repeat >= 2 || seen[sig] > 3 {
			obs := "同一调用已经重复多次，结果不会变。请换一种做法或直接给出 final。"
			step.Error = obs
			msgs = append(msgs,
				llm.TextMessage(llm.RoleAssistant, text),
				llm.UserText("【工具结果】\n"+obs))
			res.Steps = append(res.Steps, step)
			r.fire(EvToolResult, map[string]any{"index": i, "tool": toolName, "error": obs})
			continue
		}

		argsJSON, _ := json.Marshal(act.Args)
		step.Tool = toolName
		step.Args = clip(string(argsJSON), 400)
		r.fire(EvTool, map[string]any{"index": i, "tool": toolName, "args": string(argsJSON), "thought": act.Thought})

		ok, need := r.needApproval(toolName)
		if need {
			allow, aerr := r.askApproval(ctx, i, toolName, string(argsJSON))
			if aerr != nil || !allow {
				step.Skipped = true
				step.Error = "老师拒绝了这次操作"
				obs := "老师拒绝了这次操作，未能执行。请说明原因，并给出不需要改文件的替代方案。"
				msgs = append(msgs,
					llm.TextMessage(llm.RoleAssistant, text),
					llm.UserText("【工具结果】\n"+obs))
				res.Steps = append(res.Steps, step)
				r.fire(EvToolResult, map[string]any{"index": i, "tool": toolName, "error": obs})
				continue
			}
			step.Approved = true
		}
		_ = ok

		tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		t0 := time.Now()
		tr, terr := r.tools.Run(tctx, toolName, act.Args)
		cancel()
		step.Duration = time.Since(t0).Milliseconds()
		obs := tr.Content
		if terr != nil {
			obs = "工具执行失败：" + terr.Error()
			step.Error = terr.Error()
		} else if tr.Err != "" {
			obs = tr.Err
			step.Error = tr.Err
		}
		obs = clip(obs, 2400)
		step.Result = obs
		res.Steps = append(res.Steps, step)
		r.fire(EvToolResult, map[string]any{
			"index": i, "tool": toolName, "result": obs, "error": step.Error,
			"durationMs": step.Duration, "data": tr.Data,
		})
		msgs = append(msgs,
			llm.TextMessage(llm.RoleAssistant, text),
			llm.UserText("【工具结果】\n"+obs))
	}

	// 步数用尽：再要一次总结，避免老师拿到空回复。
	res.Stop = "max-steps"
	msgs = append(msgs, llm.UserText("步数已用尽。请基于上面的工具结果，直接输出 final，不要再调用工具。"))
	out, err := r.client.Chat(ctx, msgs, llm.Options{Temperature: 0.2, MaxTokens: r.cfg.Provider.MaxTokens, Subject: "课堂Agent", FollowUp: true})
	if err == nil && out != nil {
		act := parseAction(out.Text)
		res.Answer = act.Final
		if res.Answer == "" {
			res.Answer = strings.TrimSpace(out.Text)
		}
	}
	if res.Answer == "" {
		res.Answer = "这次没有得出结论。可以缩小问题范围再问一次，或直接在实录页看这一页的笔记。"
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	r.fire(EvFinal, map[string]any{"answer": res.Answer, "steps": len(res.Steps), "stop": res.Stop})
	return res, nil
}

func (r *Runner) promptExtra() string {
	var b strings.Builder
	if r.plugins != nil {
		if p := r.plugins.Prompt(); strings.TrimSpace(p) != "" {
			b.WriteString(p)
		}
		if tools := r.plugins.Tools(); len(tools) > 0 {
			b.WriteString("\n本课启用的插件建议使用的工具：")
			b.WriteString(strings.Join(tools, "、"))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// allowedTools 返回本次可用的工具名。
//
// 这里刻意**不**用「插件声明的工具集」去裁剪内置工具：插件是加法，
// 一个只读插件不该把「生成报告」这类能力从课堂助手上摘掉——那样老师
// 装了个笔记插件，反而发现 Agent 不会写报告了，属于最难排查的那类 bug。
// 插件声明的工具只作为「本课建议」，写进提示词，不构成限制。
func (r *Runner) allowedTools() []string {
	return r.tools.Names()
}

// needApproval 按权限级别判断是否需要人工确认。返回 (是否放行, 是否需询问)。
func (r *Runner) needApproval(tool string) (bool, bool) {
	spec, ok := r.tools.Spec(tool)
	if !ok {
		return false, false
	}
	switch r.cfg.Agent.Permission {
	case "all":
		return true, false
	case "review":
		return false, true
	default: // remind
		return spec.ReadOnly, !spec.ReadOnly
	}
}

func (r *Runner) askApproval(ctx context.Context, step int, tool, args string) (bool, error) {
	spec, _ := r.tools.Spec(tool)
	req := ApprovalRequest{
		ID: newApprovalID(), Tool: tool,
		Title: spec.Title, Args: clip(args, 600), Preview: previewOf(spec, args),
	}
	if r.approve == nil {
		return false, errors.New("没有可用的审批通道")
	}
	r.fire(EvApproval, req)
	return r.approve(ctx, req)
}

// newApprovalID 生成不可猜的审批 id。
//
// 之前用时间戳拼出来的 id，本机任意网页都能猜到并 POST /api/agent/approve
// 把「写笔记」这类操作直接放行。审批凭证必须是随机的。
func newApprovalID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ap-%d", time.Now().UnixNano())
	}
	return "ap-" + hex.EncodeToString(b[:])
}

// ---- 动作协议解析 ----

type action struct {
	Thought string         `json:"thought"`
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
	Final   string         `json:"final"`
}

// parseAction 从模型输出里解析一个动作。
//
// 容错顺序：完整 JSON → 代码块里的 JSON → 单层嵌套（{"action":{...}}）→
// 纯文本当 final。宁可把纯文本当回答，也不要把回答误判成工具调用。
func parseAction(text string) action {
	var a action
	raw, ok := llm.ExtractJSON(text)
	if !ok {
		return action{Final: strings.TrimSpace(text)}
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		var loose map[string]any
		if err := llm.DecodeJSON(text, &loose); err != nil {
			return action{Final: strings.TrimSpace(text)}
		}
		a = fromMap(loose)
		return a
	}
	// 有些模型会把动作包在 action 字段里。
	if a.Tool == "" && a.Final == "" {
		var wrap struct {
			Action *action `json:"action"`
		}
		if json.Unmarshal([]byte(raw), &wrap) == nil && wrap.Action != nil {
			a = *wrap.Action
		}
	}
	if a.Tool != "" {
		a.Tool = strings.TrimSpace(a.Tool)
	}
	return a
}

func fromMap(m map[string]any) action {
	a := action{}
	if v, ok := m["thought"].(string); ok {
		a.Thought = v
	}
	if v, ok := m["tool"].(string); ok {
		a.Tool = v
	}
	if v, ok := m["final"].(string); ok {
		a.Final = v
	}
	for _, k := range []string{"args", "arguments", "parameters"} {
		if v, ok := m[k].(map[string]any); ok {
			a.Args = v
			break
		}
	}
	if a.Tool == "" {
		if v, ok := m["action"].(map[string]any); ok {
			if t, ok := v["tool"].(string); ok {
				a.Tool = t
			}
			if ar, ok := v["args"].(map[string]any); ok {
				a.Args = ar
			}
		}
	}
	return a
}

// catalogText 生成提示词里的工具目录。
func catalogText(specs []ToolSpec) string {
	var b strings.Builder
	for _, s := range specs {
		fmt.Fprintf(&b, "- %s（%s）%s\n", s.Name, s.Title, s.Description)
		if len(s.Args) > 0 {
			b.WriteString("  参数：")
			parts := make([]string, 0, len(s.Args))
			for _, a := range s.Args {
				note := a.Type
				if a.Required {
					note += "，必填"
				}
				if a.Note != "" {
					note += "，" + a.Note
				}
				parts = append(parts, a.Name+"("+note+")")
			}
			b.WriteString(strings.Join(parts, "；"))
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		return "（本次没有可用工具，只能基于对话上下文回答）"
	}
	return b.String()
}

func previewOf(spec ToolSpec, args string) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(args), &m)
	if v, ok := m["preview"].(string); ok && v != "" {
		return clip(v, 300)
	}
	return clip(spec.Description+"\n"+args, 300)
}

func describe(c llm.Client) string {
	if c == nil {
		return ""
	}
	return c.Describe()
}

func sign(tool string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	h := sha1.Sum(append([]byte(tool), raw...))
	return hex.EncodeToString(h[:8])
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Logf 统一日志前缀，方便现场排障。
func Logf(format string, args ...any) { logx.Infof("agent", format, args...) }
