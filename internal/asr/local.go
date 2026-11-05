package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"classnote/internal/logx"
)

// LocalServer 托管随包分发的 whisper.cpp 服务端。
//
// 与每次调用 whisper-cli 重新加载模型不同，服务端常驻内存，
// 一次加载后每段音频只要几十到几百毫秒就能出结果，适合课堂实时转写。
type LocalServer struct {
	cmd  *exec.Cmd
	port int
	exe  string
	dir  string // 运行目录（DLL 依赖都在这里）
}

// 全局单例，应用只起一个 whisper-server。
var (
	localMu  sync.Mutex
	localSrv *LocalServer
	httpCli  = &http.Client{Timeout: 120 * time.Second}
)

// EnsureASCIIWhisperDir 确保 whisper 运行时位于纯 ASCII 路径。
//
// whisper-server.exe 是 C 程序，argv 与 fopen 走系统 ANSI 代码页，
// 路径里的中文（例如 E://私人//...）会被截成乱码导致模型加载失败。
// 因此运行时需要部署到 ASCII 目录；随包目录本身是 ASCII 时直接用。
func EnsureASCIIWhisperDir(bundled string) (string, error) {
	if bundled == "" {
		return "", fmt.Errorf("未找到 whisper 运行时目录")
	}
	if isASCIIPath(bundled) {
		return bundled, nil
	}
	target := filepath.Join(os.Getenv("ProgramData"), "ClassNote", "whisper")
	if target == "" || !isASCIIPath(target) {
		target = `C://ProgramData//ClassNote//whisper`
	}
	// 已经部署过也继续检查：新版本可能补充了 CPU 后端 DLL。
	if err := os.MkdirAll(filepath.Join(target, "models"), 0o755); err != nil {
		return "", fmt.Errorf("创建语音模型目录失败: %w", err)
	}
	// 先复制二进制，再复制模型（模型大，放在最后）。
	files := []string{
		"whisper-server.exe", "whisper-cli.exe", "whisper.dll",
		"ggml.dll", "ggml-base.dll", "SDL2.dll",
	}
	for _, f := range files {
		src := filepath.Join(bundled, f)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(target, f)
		if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
			continue
		}
		if err := copyFileSimple(src, dst); err != nil {
			return "", fmt.Errorf("复制 %s 失败: %w", f, err)
		}
	}
	// 复制全部 CPU 后端（ggml-cpu-alderlake / haswell / ...）。
	// whisper.cpp 会按 CPU 指令集自动挑最快的，只带 x64 通用版会慢一个数量级。
	backends, _ := filepath.Glob(filepath.Join(bundled, "ggml-cpu-*.dll"))
	for _, src := range backends {
		name := filepath.Base(src)
		dst := filepath.Join(target, name)
		if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
			continue
		}
		if err := copyFileSimple(src, dst); err != nil {
			return "", fmt.Errorf("复制 %s 失败: %w", name, err)
		}
	}
	models, _ := os.ReadDir(filepath.Join(bundled, "models"))
	for _, m := range models {
		if m.IsDir() || !strings.HasSuffix(strings.ToLower(m.Name()), ".bin") {
			continue
		}
		dst := filepath.Join(target, "models", m.Name())
		if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
			continue
		}
		src := filepath.Join(bundled, "models", m.Name())
		if err := copyFileSimple(src, dst); err != nil {
			return "", fmt.Errorf("复制模型 %s 失败: %w", m.Name(), err)
		}
	}
	return target, nil
}

// isASCIIPath 判断路径是否只含 ASCII 字符。
func isASCIIPath(p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] > 0x7F {
			return false
		}
	}
	return true
}

func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// LocalDefaultDir 返回随包 whisper 运行时目录。
func LocalDefaultDir(appRoot string) string {
	for _, c := range []string{
		filepath.Join(appRoot, "integrations", "whisper"),
		filepath.Join(appRoot, "whisper"),
		filepath.Join(appRoot, "runtime", "whisper"),
	} {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return filepath.Join(appRoot, "integrations", "whisper")
}

// LocalModels 列出可用的本地模型文件。
func LocalModels(dir string) []string {
	var out []string
	entries, _ := os.ReadDir(filepath.Join(dir, "models"))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".bin") {
			continue
		}
		out = append(out, filepath.Join(dir, "models", e.Name()))
	}
	return out
}

// StartLocal 启动 whisper-server（若已在运行则复用），按中文识别。
func StartLocal(dir, model string, port int) (*LocalServer, error) {
	return StartLocalLang(dir, model, port, "zh")
}

// StartLocalLang 启动 whisper-server，可指定识别语言。
//
// 早先这里把 -l zh 写死了，英语课、日语课上会被强行转成汉字。
func StartLocalLang(dir, model string, port int, lang string) (*LocalServer, error) {
	localMu.Lock()
	defer localMu.Unlock()
	if localSrv != nil && localSrv.Alive() {
		return localSrv, nil
	}
	if dir == "" {
		return nil, fmt.Errorf("未找到本地语音模型目录")
	}
	exe := filepath.Join(dir, "whisper-server.exe")
	if _, err := os.Stat(exe); err != nil {
		return nil, fmt.Errorf("缺少 whisper-server.exe，请重新安装或检查 %s", dir)
	}
	if model == "" {
		models := LocalModels(dir)
		if len(models) == 0 {
			return nil, fmt.Errorf("目录里没有模型文件（models/*.bin）")
		}
		model = models[0]
	}
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("模型文件不存在: %s", model)
	}
	if port <= 0 {
		port = 18095
	}
	// 端口被占用且健康 → 复用（可能是上次未正常退出的实例）
	if LocalHealth(port) {
		s := &LocalServer{port: port, exe: exe, dir: dir}
		localSrv = s
		logx.Infof("asr", "复用已在运行的 whisper-server（端口 %d）", port)
		return s, nil
	}

	// 默认 4 线程在 12 代酷睿上仍然偏慢；按逻辑核心数给足线程，
	// 同时限制上限，避免在低功耗机器上抢占课堂其他程序。
	threads := runtime.NumCPU()
	if threads < 4 {
		threads = 4
	}
	if threads > 16 {
		threads = 16
	}
	cmd := exec.Command(exe,
		"-m", model,
		"-l", normalizeLang(lang),
		"-t", strconv.Itoa(threads),
		"--port", strconv.Itoa(port),
		"--host", "127.0.0.1",
		"--no-timestamps",
	)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// 把输出落到日志文件，便于排查启动失败原因。
	var logFile *os.File
	if lf, err := os.OpenFile(filepath.Join(dir, "whisper-server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		logFile = lf
		cmd.Stdout = lf
		cmd.Stderr = lf
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, fmt.Errorf("启动 whisper-server 失败: %w", err)
	}
	// 必须在 Start 之后再 Wait，否则会立刻结束并把日志句柄关掉。
	go func() {
		_ = cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		logx.Infof("asr", "whisper-server 进程已退出")
	}()
	s := &LocalServer{cmd: cmd, port: port, exe: exe, dir: dir}
	logx.Infof("asr", "正在启动 whisper-server（%s）...", filepath.Base(model))
	// 等待就绪
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if LocalHealth(port) {
			localSrv = s
			logx.Infof("asr", "本地语音模型已就绪（端口 %d，模型 %s）", port, filepath.Base(model))
			return s, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("whisper-server 启动超时")
}

// Alive 判断服务进程是否还在。
func (s *LocalServer) Alive() bool {
	if s == nil {
		return false
	}
	if s.cmd == nil || s.cmd.Process == nil {
		// 复用外部实例的情况
		return LocalHealth(s.port)
	}
	// Windows 上进程退出后仍可 Wait，这里用健康探测更可靠
	return LocalHealth(s.port)
}

// Port 返回服务端口。
func (s *LocalServer) Port() int {
	if s == nil {
		return 0
	}
	return s.port
}

// Stop 停止服务。
func (s *LocalServer) Stop() {
	if s == nil {
		return
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	// 全局单例也要一起清掉，否则下次 StartLocal 会「复用」一个死掉的实例。
	localMu.Lock()
	if localSrv == s {
		localSrv = nil
	}
	localMu.Unlock()
}

// RestartLocal 换模型：停掉旧进程，用新模型重新起一个。
//
// 模型在 whisper-server 启动时就加载进内存，换文件必须重启。
func RestartLocal(dir, model string, port int, lang string) (*LocalServer, error) {
	localMu.Lock()
	old := localSrv
	localSrv = nil
	localMu.Unlock()
	if old != nil {
		old.Stop()
	}
	if port <= 0 {
		port = 18095
	}
	// 等旧进程真正退出。Windows 释放端口有一小段延迟，
	// 不等的话新进程 bind 失败，表现就是「换完模型没声音了」。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !LocalHealth(port) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if LocalHealth(port) {
		return nil, fmt.Errorf("旧语音服务没能停下来（端口 %d 仍被占用）", port)
	}
	return StartLocalLang(dir, model, port, lang)
}

// normalizeLang 把配置里的语言代码映射成 whisper.cpp 认的写法。
func normalizeLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "", "zh", "zh-cn", "zh-hans", "cn", "chinese", "中文":
		return "zh"
	case "auto", "detect":
		return "auto"
	}
	return l
}

// runningLocal 加锁读取全局实例，避免与 RestartLocal 打架。
func runningLocal() *LocalServer {
	localMu.Lock()
	defer localMu.Unlock()
	return localSrv
}

// LocalHealth 探测 whisper-server 是否可用。
func LocalHealth(port int) bool {
	cli := &http.Client{Timeout: 2 * time.Second}
	resp, err := cli.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// LocalTranscribe 调用 whisper-server 转写 WAV 字节。
func LocalTranscribe(ctx context.Context, port int, wav []byte, language string) (string, error) {
	if !LocalHealth(port) {
		return "", fmt.Errorf("本地语音服务未运行")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "chunk.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	if language == "" {
		language = "zh"
	}
	_ = mw.WriteField("language", language)
	_ = mw.WriteField("response_format", "json")
	_ = mw.WriteField("temperature", "0")
	// 不让窗口之间互相带上下文：课堂片段通常 12 秒以内、只有一个窗口，
	// 这条基本不生效；真碰上长段连读时，它挡掉的是"上一窗结尾被当成下一窗
	// 开头"进而整句复读的老毛病。
	_ = mw.WriteField("no_context", "true")
	// 这里原本发的是「课堂讲课内容，语文、化学、物理等学科术语。」
	// whisper.cpp 会把 prompt 当成上文，静音段上直接把它续写成正文，
	// 表现就是转写里凭空多出一句「有关语文、数学、英语等……」。
	// 见 asr.go 里同一处改动的原因说明。
	if err := mw.Close(); err != nil {
		return "", err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/inference", port), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := httpCli.Do(req)
	if err != nil {
		return "", fmt.Errorf("本地转写请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("本地转写返回 %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析本地转写结果失败: %s", truncate(string(raw), 160))
	}
	return strings.TrimSpace(out.Text), nil
}

// RunningServer 返回当前运行中的 whisper-server，没有则返回 nil。
func RunningServer() *LocalServer { return runningLocal() }
