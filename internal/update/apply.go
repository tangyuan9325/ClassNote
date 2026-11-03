package update

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"classnote/internal/appinfo"
	"classnote/internal/logx"
)

const (
	// UpdatesDir 是 <root>/data 下的更新工作目录。
	UpdatesDir = "updates"
	planName   = "apply-plan.json"
	resultName = "last-update.json"
)

// Plan 是主程序交给辅助进程的升级计划。
//
// 之所以要落盘而不是塞命令行：路径里可能有空格和中文，
// 命令行长度也有上限，而这份计划还要给「失败后人工排查」留证据。
type Plan struct {
	Root    string    `json:"root"`
	Payload string    `json:"payload"` // 解压后的内容目录
	Staging string    `json:"staging"` // 整个解压目录（成功后删）
	Backup  string    `json:"backup"`
	Log     string    `json:"log"`
	Result  string    `json:"result"`
	OldPID  int       `json:"oldPid"`
	Version string    `json:"version"`
	From    string    `json:"from"`
	Args    []string  `json:"args"`
	WaitMS  int       `json:"waitMs"` // 等主程序退出的上限，默认 90s
	Started time.Time `json:"started"`
}

// Result 是升级结果，写到 <root>/data/updates/last-update.json。
//
// 主程序下次启动会读它：成功了就提示「已更新到 x.y.z」，
// 失败了就把原因摆出来——不然用户只会看到程序「莫名其妙没了」。
type Result struct {
	OK         bool      `json:"ok"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	At         time.Time `json:"at"`
	Files      int       `json:"files"`
	RolledBack bool      `json:"rolledBack,omitempty"`
	Restarted  bool      `json:"restarted"`
	Error      string    `json:"error,omitempty"`
	Log        string    `json:"log,omitempty"`
}

// ApplyInfo 是 Apply 成功后返回的信息。
type ApplyInfo struct {
	HelperPID int    `json:"helperPid"`
	Files     int    `json:"files"`
	From      string `json:"from"`
	To        string `json:"to"`
	Payload   string `json:"payload"`
	Log       string `json:"log"`
	Result    string `json:"result"`
}

// Dir 返回更新工作目录。
func Dir(root string) string { return filepath.Join(root, "data", UpdatesDir) }

// ResultPath 返回升级结果文件路径。
func ResultPath(root string) string { return filepath.Join(Dir(root), resultName) }

// Apply 解压更新包并拉起辅助升级进程。
//
// 主程序不需要自己覆盖文件：它自己就锁着 ClassNote.exe，
// 而且更新过程中一旦崩溃就会留下半个版本。真正干活的是
// 一个复制出去的助手进程，主程序只负责躺平等着被替换。
func Apply(zipPath, root, version string) (ApplyInfo, error) {
	var info ApplyInfo
	info.From = strings.TrimPrefix(appinfo.Version, "v")
	info.To = strings.TrimPrefix(version, "v")

	// 先确认目录可写：装在 Program Files 里的程序没有写权限，
	// 这时候直接报错，比让用户等程序退出后才发现失败好得多。
	if err := writeProbe(root); err != nil {
		return info, fmt.Errorf("程序目录不可写（%s）。请把 ClassNote 放到自己有权修改的目录，或以管理员身份重新运行后再更新", root)
	}

	payload, staging, files, err := Stage(zipPath, root)
	if err != nil {
		return info, err
	}
	info.Files = files
	info.Payload = payload

	dir := Dir(root)
	_ = os.MkdirAll(dir, 0o755)
	stamp := time.Now().Format("20060102-150405")
	plan := Plan{
		Root:    root,
		Payload: payload,
		// 只能是 staging 本身：这里若写成 payload 的父目录，
		// 平铺的发布包（内容直接在 zip 根上）会把 data/updates 整个删掉，
		// 连带刚写下的升级日志和结果文件一起消失。
		Staging: staging,
		Backup:  filepath.Join(dir, "backup-"+stamp),
		Log:     filepath.Join(dir, "update-"+stamp+".log"),
		Result:  filepath.Join(dir, resultName),
		OldPID:  os.Getpid(),
		Version: info.To,
		From:    info.From,
		Args:    restartArgs(os.Args[1:]),
		WaitMS:  90000,
		Started: time.Now(),
	}
	planPath := filepath.Join(dir, planName)
	if err := writeJSON(planPath, plan); err != nil {
		return info, fmt.Errorf("写入升级计划失败: %w", err)
	}
	info.Log, info.Result = plan.Log, plan.Result

	helper, err := prepareHelper(dir, payload)
	if err != nil {
		return info, err
	}
	pid, err := startHelper(helper, planPath, root)
	if err != nil {
		return info, fmt.Errorf("启动升级助手失败: %w", err)
	}
	info.HelperPID = pid

	// 握手：助手一启动就会往日志里写 READY。等不到就说明它没能跑起来
	// （杀软拦截、目录被策略禁用），这时候还不能退出主程序。
	if !waitHelperReady(plan.Log, pid, 6*time.Second) {
		return info, fmt.Errorf("升级助手没有响应（可能被杀毒软件拦下）。请把 ClassNote 目录加入白名单后重试，本次未改动任何文件")
	}
	logx.Infof("update", "升级助手已就绪（PID %d），日志 %s", pid, plan.Log)
	return info, nil
}

// Stage 解压并校验更新包，返回内容目录、暂存根目录与文件数。
//
// 返回两个目录是因为发布包有两种摆法：内容直接在 zip 根上
// （build.ps1 的产物），或者套了一层版本目录。清理时要删的永远是
// 暂存根目录，而不是内容目录——平铺时两者相同，嵌套时后者是子目录。
func Stage(zipPath, root string) (payload, staging string, files int, err error) {
	staging = filepath.Join(Dir(root), "staging")
	_ = os.RemoveAll(staging)
	if err := unzip(zipPath, staging); err != nil {
		return "", "", 0, fmt.Errorf("解压失败: %w", err)
	}
	content := staging
	entries, _ := os.ReadDir(staging)
	if len(entries) == 1 && entries[0].IsDir() {
		content = filepath.Join(staging, entries[0].Name())
	}
	if err := verifyPayload(content); err != nil {
		_ = os.RemoveAll(staging)
		return "", "", 0, err
	}
	return content, staging, countFiles(content), nil
}

// prepareHelper 准备辅助进程的可执行文件。
//
// 优先复制「自己」：当前正在运行的这份是已知可用的，复制出来的副本
// 不占安装目录里的 ClassNote.exe，替换时互不干扰。复制失败（极少数
// 情况是被安全软件锁读）就退回用更新包里那个新版本自己当助手。
func prepareHelper(dir, payload string) (string, error) {
	// 先清掉上一轮的助手副本：它可能还在跑，删不掉也无所谓，
	// 但绝不能因为一个旧文件把这次更新卡住。
	for _, m := range oldHelpers(dir) {
		_ = os.Remove(m)
	}
	helper := filepath.Join(dir, fmt.Sprintf("updater-%d.exe", time.Now().UnixNano()))
	self, err := os.Executable()
	if err == nil {
		if err := copyFile(self, helper); err == nil {
			return helper, nil
		}
	}
	src := filepath.Join(payload, "ClassNote.exe")
	if err := copyFile(src, helper); err != nil {
		return "", fmt.Errorf("无法准备升级助手: %w", err)
	}
	return helper, nil
}

// oldHelpers 列出残留的助手副本。
func oldHelpers(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "updater-*.exe"))
	out := matches[:0]
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && time.Since(fi.ModTime()) > time.Minute {
			out = append(out, m)
		}
	}
	return out
}

// restartArgs 过滤掉更新相关参数，重启时原样带回其余参数。
func restartArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == ApplyFlag {
			i++ // 跳过它的值
			continue
		}
		if strings.HasPrefix(a, ApplyFlag+"=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func waitHelperReady(logPath string, pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(logPath); err == nil && strings.Contains(string(b), "READY") {
			return true
		}
		if !alive(pid) {
			// 进程都没了，再等也没有意义；再给一次读日志的机会。
			if b, err := os.ReadFile(logPath); err == nil && strings.Contains(string(b), "READY") {
				return true
			}
			return false
		}
		time.Sleep(120 * time.Millisecond)
	}
	return false
}

// LastResult 读取上一次升级结果。
func LastResult(root string) (Result, bool) {
	var r Result
	b, err := os.ReadFile(ResultPath(root))
	if err != nil {
		return r, false
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, false
	}
	return r, true
}

// ---- 文件工具 ----

func writeProbe(dir string) error {
	f, err := os.CreateTemp(dir, ".update-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// 结果文件在 data/updates 下，第一次升级时这个目录可能还不存在。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func countFiles(dir string) int {
	n := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	// 更新包单个文件最大 200MB：解压炸弹式的包在复制这一步就停住。
	if _, err := io.Copy(out, io.LimitReader(in, 400<<20)); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	var total int64
	var lastErr error
	for _, f := range r.File {
		target := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) {
			continue // 防 zip slip
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			continue
		}
		// 单文件限 400MB，累计限 800MB：解压炸弹会在这一步停下。
		written, cerr := io.Copy(out, io.LimitReader(rc, 400<<20))
		out.Close()
		rc.Close()
		total += written
		if cerr != nil || total > 800<<20 {
			_ = os.Remove(target)
			if cerr != nil {
				lastErr = cerr
			} else {
				lastErr = fmt.Errorf("更新包解压后超过 800MB，已中止")
				break
			}
		}
	}
	return lastErr
}

// verifyPayload 确认解压目录里有一个像样的 ClassNote.exe。
//
// 没有这一步，任何一个 zip 都会被覆盖进安装目录——
// 更新包来源只有 GitHub Releases，但这仍是最便宜的一道闸。
func verifyPayload(dir string) error {
	path := filepath.Join(dir, "ClassNote.exe")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("更新包里没有 ClassNote.exe，已取消升级")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < 1<<20 {
		return fmt.Errorf("更新包里的 ClassNote.exe 大小异常，已取消升级")
	}
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil || head[0] != 'M' || head[1] != 'Z' {
		return fmt.Errorf("更新包里的 ClassNote.exe 不是有效的 Windows 程序，已取消升级")
	}
	return nil
}

// updLog 是辅助进程的日志：同时写文件和 stderr。
//
// 文件留证据（用户可以把日志发过来），stderr 方便命令行调试时直接看。
type updLog struct {
	mu sync.Mutex
	f  *os.File
}

func newUpdLog(path string) *updLog {
	l := &updLog{}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		l.f = f
	}
	return l
}

func (l *updLog) printf(format string, a ...any) {
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	l.mu.Lock()
	if l.f != nil {
		_, _ = l.f.WriteString(line)
		_ = l.f.Sync()
	}
	l.mu.Unlock()
	fmt.Fprint(os.Stderr, line)
}

func (l *updLog) close() {
	l.mu.Lock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
	l.mu.Unlock()
}
