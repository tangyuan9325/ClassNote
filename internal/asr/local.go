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
	// 已经部署过就直接用（模型 180MB，不重复复制）。
	if _, err := os.Stat(filepath.Join(target, "whisper-server.exe")); err == nil {
		if len(LocalModels(target)) > 0 {
			return target, nil
		}
	}
	if err := os.MkdirAll(filepath.Join(target, "models"), 0o755); err != nil {
		return "", fmt.Errorf("创建语音模型目录失败: %w", err)
	}
	// 先复制二进制，再复制模型（模型大，放在最后）。
	files := []string{
		"whisper-server.exe", "whisper-cli.exe", "whisper.dll",
		"ggml.dll", "ggml-base.dll", "ggml-cpu-x64.dll", "SDL2.dll",
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

// StartLocal 启动 whisper-server（若已在运行则复用）。
func StartLocal(dir, model string, port int) (*LocalServer, error) {
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

	cmd := exec.Command(exe,
		"-m", model,
		"-l", "zh",
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
	localSrv = nil
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
	_ = mw.WriteField("prompt", "课堂讲课内容，语文、化学、物理等学科术语。")
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
