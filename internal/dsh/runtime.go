// Package dsh 管理 DeepSeek Harness 运行时。
//
// 为什么需要它：DSH 的插件是 npm 包，只能由 DSH 自己加载。ClassNote 想把
// 「DSH 渠道」的插件真正跑起来，就必须在自己身边带一个 DSH 运行时。
//
// 三条硬约束（都实测过，不是猜的）：
//
//  1. Node 必须 ≥ 22.19。DSH 的入口用了 `import.meta.main`，低版本 Node
//     读不懂这个属性，CLI 会 **静默退出、exit code 0、没有任何输出**。
//     这是最容易误判成「插件坏了」的坑，所以这里一定要把版本卡住并说人话。
//  2. 运行时约 600 MB（608 个包）。不可能塞进安装包，只能按需安装到
//     data/dsh 下。
//  3. 装插件走 `dsh plugin --profile web add`，它内部转发 pnpm；pnpm 由
//     Node 自带的 corepack 提供，不污染系统环境。
package dsh

import (
	"archive/zip"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dshLicense 是 DeepSeek Harness 的 MIT 许可原文。
//
// 运行时是按需从 npm 装的，但许可义务不因此免除：
// 装好之后要在本地留一份，老师（或律师）翻得到。
//
//go:embed DSH-LICENSE.txt
var dshLicense string

// MinNode 是 DSH 要求的 Node 下限。
//
// 22.19 是 `import.meta.main` 进入 Node 22 线的版本；更低的 22.x 会静默失败。
const MinNode = "22.19.0"

// NodeVersion 是本程序自动下载的 Node 版本（LTS）。
const NodeVersion = "24.22.2"

// 阶段名，前端直接拿去显示。
const (
	PhaseIdle       = "idle"       // 没装运行时
	PhaseDownload   = "download"   // 正在下载 Node
	PhaseInstalling = "installing" // 正在 npm install
	PhaseStarting   = "starting"   // 正在启动
	PhaseRunning    = "running"    // 已就绪
	PhaseStopped    = "stopped"    // 装好了但没启动
	PhaseError      = "error"
)

// Status 是给界面看的一份快照。
type Status struct {
	Supported bool     `json:"supported"`
	Phase     string   `json:"phase"`
	Installed bool     `json:"installed"`
	Running   bool     `json:"running"`
	NodePath  string   `json:"nodePath"`
	NodeVer   string   `json:"nodeVersion"`
	NodeOK    bool     `json:"nodeOk"`
	MinNode   string   `json:"minNode"`
	Pnpm      bool     `json:"pnpm"`
	DshVer    string   `json:"dshVersion"`
	Port      int      `json:"port"`
	URL       string   `json:"url"`
	Root      string   `json:"root"`
	Error     string   `json:"error"`
	Busy      bool     `json:"busy"`
	Plugins   []string `json:"plugins"`
	Log       []string `json:"log"`
}

// Runtime 管着一个 DSH 子进程。
type Runtime struct {
	root string
	emit func(line string) // 日志外送（服务端拿去推 SSE）

	mu      sync.Mutex
	phase   string
	errMsg  string
	busy    bool
	logs    []string
	cmd     *exec.Cmd
	node    string // node 可执行文件绝对路径
	nodeVer string
	port    int
	token   string
	started bool

	// node 探测结果缓存：界面每两秒轮询一次，不能每次都去 exec 一遍 node -v。
	probeAt  time.Time
	probeErr string
}

// probeTTL 是 node 探测结果的缓存时长。
const probeTTL = 60 * time.Second

// InvalidateProbe 丢弃探测缓存（刚装完 Node 或改过 PATH 后调用）。
func (r *Runtime) InvalidateProbe() {
	r.mu.Lock()
	r.probeAt = time.Time{}
	r.mu.Unlock()
}

// probeNode 带缓存地找 node。
func (r *Runtime) probeNode() (string, string, error) {
	r.mu.Lock()
	if !r.probeAt.IsZero() && time.Since(r.probeAt) < probeTTL {
		path, ver, msg := r.node, r.nodeVer, r.probeErr
		r.mu.Unlock()
		if path != "" && msg == "" {
			return path, ver, nil
		}
		if msg != "" {
			return "", "", errors.New(msg)
		}
	} else {
		r.mu.Unlock()
	}

	path, ver, err := r.FindNode()

	r.mu.Lock()
	r.probeAt = time.Now()
	if err != nil {
		r.node, r.nodeVer, r.probeErr = "", "", err.Error()
	} else {
		r.node, r.nodeVer, r.probeErr = path, ver, ""
	}
	r.mu.Unlock()
	return path, ver, err
}

// New 创建运行时管理器。root 一般是 <数据目录>/dsh。
func New(root string, emit func(string)) *Runtime {
	if emit == nil {
		emit = func(string) {}
	}
	return &Runtime{root: root, emit: emit, phase: PhaseIdle}
}

// Root 返回运行时的安装目录。
func (r *Runtime) Root() string { return r.root }

func (r *Runtime) addLog(line string) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return
	}
	// 日志会被多个协程同时写（stdout 读协程、安装流程、HTTP 线程），
	// 早先没加锁，Status 并发读的时候是实打实的数据竞争。
	r.mu.Lock()
	r.logs = append(r.logs, line)
	if len(r.logs) > 200 {
		r.logs = r.logs[len(r.logs)-200:]
	}
	emit := r.emit
	r.mu.Unlock()
	if emit != nil {
		emit(line)
	}
}

// Log 追加一行日志（供外部命令输出复用）。
func (r *Runtime) Log(line string) { r.addLog(line) }

func (r *Runtime) setPhase(p string) {
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
}

// nodeExeName 返回当前平台的 node 可执行文件名。
func nodeExeName() string {
	if runtime.GOOS == "windows" {
		return "node.exe"
	}
	return "node"
}

// bundledNode 返回随程序下载的 node 路径（可能不存在）。
func (r *Runtime) bundledNode() string {
	return filepath.Join(r.root, "node", nodeExeName())
}

// entry 返回 DSH 的 CLI 入口（可能不存在）。
func (r *Runtime) entry() string {
	return filepath.Join(r.root, "runtime", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
}

// Installed 判断运行时是否已经装好。
func (r *Runtime) Installed() bool {
	if _, err := os.Stat(r.entry()); err != nil {
		return false
	}
	// 主包在，但依赖可能被误删；抽查一个必需的依赖。
	_, err := os.Stat(filepath.Join(r.root, "runtime", "node_modules", "@deepseek-ai", "dsh-app-boot"))
	return err == nil
}

// FindNode 找到可用的 node：优先自带的，其次系统 PATH。
func (r *Runtime) FindNode() (string, string, error) {
	candidates := []string{r.bundledNode()}
	if p, err := exec.LookPath(nodeExeName()); err == nil {
		candidates = append(candidates, p)
	}
	// Windows 上常见安装位置：找不到 PATH 时再看一眼。
	if runtime.GOOS == "windows" {
		for _, base := range []string{`C:\Program Files\nodejs`, `C:\Program Files (x86)\nodejs`} {
			candidates = append(candidates, filepath.Join(base, "node.exe"))
		}
	}
	var lastErr error = errors.New("没有找到 Node.js")
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err != nil {
			continue
		}
		ver, err := nodeVersion(c)
		if err != nil {
			lastErr = fmt.Errorf("%s 无法执行：%w", c, err)
			continue
		}
		if !VersionAtLeast(ver, MinNode) {
			lastErr = fmt.Errorf("Node %s 版本太低（需要 ≥ %s），DSH 会静默退出", ver, MinNode)
			continue
		}
		return c, ver, nil
	}
	return "", "", lastErr
}

// nodeVersion 跑 `node -v` 拿版本。
func nodeVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-v")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var verRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// VersionAtLeast 比较版本号，b 形如 "22.19.0"。
//
// 只比前三段：Node 的版本号后面还可能跟 -nightly 之类的后缀，忽略即可。
func VersionAtLeast(a, b string) bool {
	av, ok1 := parseVer(a)
	bv, ok2 := parseVer(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if av[i] != bv[i] {
			return av[i] > bv[i]
		}
	}
	return true
}

func parseVer(s string) ([3]int, bool) {
	var out [3]int
	m := verRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Status 汇总当前状态。
func (r *Runtime) Status() Status {
	r.mu.Lock()
	st := Status{
		Phase: r.phase, Error: r.errMsg, Busy: r.busy,
		Port: r.port, URL: r.urlLocked(), Running: r.started,
		NodePath: r.node, NodeVer: r.nodeVer,
		MinNode: MinNode, Root: r.root,
	}
	st.Log = append([]string(nil), r.logs...)
	r.mu.Unlock()

	st.Supported = runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	st.Installed = r.Installed()
	if st.Installed && st.Phase == PhaseIdle {
		// 装好了但进程没起：状态该说「已就绪」，不是「未安装」。
		st.Phase = PhaseStopped
	}
	if node, ver, err := r.probeNode(); err == nil {
		st.NodePath, st.NodeVer, st.NodeOK = node, ver, true
	} else if st.NodeVer == "" {
		// 没找到 node 时把原因挂出去，但不算致命：界面还能引导下载。
		if st.Error == "" {
			st.Error = err.Error()
		}
	}
	st.Pnpm = r.hasPnpm()
	if st.Installed {
		st.DshVer = r.dshVersion()
	}
	st.Plugins = r.Plugins()
	if st.Running && st.Phase == PhaseRunning {
		st.Phase = PhaseRunning
	}
	return st
}

// urlLocked 返回带令牌的地址（调用方已持锁）。
func (r *Runtime) urlLocked() string {
	if r.port == 0 || r.token == "" {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", r.port, r.token)
}

// URL 返回带令牌的一次性地址。
func (r *Runtime) URL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.urlLocked()
}

func (r *Runtime) dshVersion() string {
	raw, err := os.ReadFile(filepath.Join(r.root, "runtime", "node_modules", "@deepseek-ai", "dsh", "package.json"))
	if err != nil {
		return ""
	}
	m := regexp.MustCompile(`"version"\s*:\s*"([^"]+)"`).FindSubmatch(raw)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// hasPnpm 判断能不能装插件。
//
// `dsh plugin` 内部转发 pnpm：先看随包的 corepack shim，再看 PATH。
func (r *Runtime) hasPnpm() bool {
	name := "pnpm"
	if runtime.GOOS == "windows" {
		name = "pnpm.cmd"
	}
	if _, err := os.Stat(filepath.Join(r.root, "node", name)); err == nil {
		return true
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// Plugins 读 profile 里已安装的插件名。
func (r *Runtime) Plugins() []string {
	raw, err := os.ReadFile(filepath.Join(r.root, "home", "profiles", "web", "package.json"))
	if err != nil {
		return nil
	}
	m := regexp.MustCompile(`(?s)"dependencies"\s*:\s*\{(.*?)\}`).FindSubmatch(raw)
	if m == nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(m[1]), ",") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.Trim(strings.TrimSpace(parts[0]), `"`)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// portFree 找一个空闲端口，从 3080 起（DSH 的默认端口）。
func portFree() int {
	for p := 3080; p < 3180; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			_ = ln.Close()
			return p
		}
	}
	return 3080
}

// InstallNode 下载官方 Node 便携包并解压到 root/node。
//
// 用便携 zip 而不是安装器：不需要管理员权限，也不改系统的 PATH。
func (r *Runtime) InstallNode(ctx context.Context) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("自动下载 Node 只支持 Windows；当前系统请先自行安装 Node ≥ %s", MinNode)
	}
	r.setPhase(PhaseDownload)
	defer func() {
		r.mu.Lock()
		p := r.phase
		r.mu.Unlock()
		if p == PhaseDownload {
			r.setPhase(PhaseIdle)
		}
	}()

	dir := filepath.Join(r.root, "downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("node-v%s-win-x64", NodeVersion)
	url := fmt.Sprintf("https://nodejs.org/dist/v%s/%s.zip", NodeVersion, name)
	zipPath := filepath.Join(dir, name+".zip")

	r.addLog("下载 Node " + NodeVersion + "（约 35 MB）")
	if err := download(ctx, url, zipPath, r.addLog); err != nil {
		return fmt.Errorf("下载 Node 失败：%w", err)
	}
	r.addLog("解压 Node 运行时")
	if err := unzipStrip(zipPath, filepath.Join(r.root, "node"), name+"/"); err != nil {
		return fmt.Errorf("解压 Node 失败：%w", err)
	}
	_ = os.Remove(zipPath)
	if _, err := os.Stat(r.bundledNode()); err != nil {
		return fmt.Errorf("解压后没有找到 node.exe")
	}
	r.enablePnpm()
	r.addLog("Node 就绪")
	return nil
}

// enablePnpm 用 corepack 生成 pnpm shim（写到我们自己的 node 目录，不动系统）。
func (r *Runtime) enablePnpm() {
	if r.hasPnpm() {
		return
	}
	node := r.bundledNode()
	corepack := filepath.Join(r.root, "node", "node_modules", "corepack", "dist", "corepack.js")
	if _, err := os.Stat(corepack); err != nil {
		r.addLog("没有找到 corepack，插件安装需要系统里有 pnpm")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, corepack, "enable", "pnpm", "--install-directory", filepath.Join(r.root, "node"))
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		r.addLog(line)
	}
	if err != nil {
		r.addLog("corepack 启用 pnpm 失败：" + err.Error())
		return
	}
	r.addLog("pnpm 就绪")
}

// Install 安装 DSH 运行时。
func (r *Runtime) Install(ctx context.Context) error {
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return fmt.Errorf("已经有一个安装任务在跑")
	}
	r.busy, r.errMsg = true, ""
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
	}()

	node, ver, err := r.probeNode()
	if err != nil {
		if runtime.GOOS == "windows" {
			if e := r.InstallNode(ctx); e != nil {
				r.fail(e.Error())
				return e
			}
			r.InvalidateProbe()
			node, ver, err = r.probeNode()
		}
		if err != nil {
			r.fail(err.Error())
			return err
		}
	}
	r.mu.Lock()
	r.node, r.nodeVer = node, ver
	r.mu.Unlock()

	r.setPhase(PhaseInstalling)
	dir := filepath.Join(r.root, "runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.fail(err.Error())
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"classnote-dsh","private":true}`), 0o644); err != nil {
		r.fail(err.Error())
		return err
	}
	r.addLog("安装 DSH 运行时（约 600 MB，第一次会比较久）")

	npm := filepath.Join(filepath.Dir(node), "node_modules", "npm", "bin", "npm-cli.js")
	var cmd *exec.Cmd
	if _, err := os.Stat(npm); err == nil {
		cmd = exec.CommandContext(ctx, node, npm, "install", "@deepseek-ai/dsh",
			"--no-audit", "--no-fund", "--omit=dev", "--loglevel=http")
	} else {
		// PATH 里的 node 没有自带 npm 布局，退回直接用 npm 命令。
		name := "npm"
		if runtime.GOOS == "windows" {
			name = "npm.cmd"
		}
		if _, lookErr := exec.LookPath(name); lookErr != nil {
			msg := "找不到 npm，无法安装 DSH 运行时"
			r.fail(msg)
			return errors.New(msg)
		}
		cmd = exec.CommandContext(ctx, name, "install", "@deepseek-ai/dsh",
			"--no-audit", "--no-fund", "--omit=dev", "--loglevel=http")
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd, done := pipeTo(cmd, r.addLog)
	hideWindow(cmd)
	err = cmd.Run()
	done()
	if err != nil {
		msg := "安装 DSH 运行时失败：" + err.Error()
		r.fail(msg)
		return errors.New(msg)
	}
	if !r.Installed() {
		msg := "安装结束但没有找到 DSH 入口，请查看日志"
		r.fail(msg)
		return errors.New(msg)
	}
	r.writeLicense()
	r.addLog("DSH 运行时安装完成")
	r.setPhase(PhaseStopped)
	return nil
}

func (r *Runtime) fail(msg string) {
	r.mu.Lock()
	r.phase, r.errMsg = PhaseError, msg
	r.mu.Unlock()
	r.addLog(msg)
}

var tokenRe = regexp.MustCompile(`token=([A-Za-z0-9_-]{16,})`)

// Start 启动 DSH Web 并等到它打印带令牌的地址。
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	if !r.Installed() {
		return fmt.Errorf("还没安装 DSH 运行时")
	}
	node, ver, err := r.probeNode()
	if err != nil {
		return err
	}

	r.setPhase(PhaseStarting)
	port := portFree()
	home := filepath.Join(r.root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}

	cmd := exec.Command(node, r.entry(), "web", "--no-open", "--port", strconv.Itoa(port))
	cmd.Dir = filepath.Join(r.root, "runtime")
	cmd.Env = append(os.Environ(),
		"DSH_HOME="+home,
		"PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	hideWindow(cmd)
	setPgid(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		r.fail("启动 DSH 失败：" + err.Error())
		return err
	}
	r.mu.Lock()
	r.cmd, r.port, r.node, r.nodeVer, r.errMsg = cmd, port, node, ver, ""
	r.mu.Unlock()

	// 从输出里抓令牌；DSH 打印的那一行形如
	//   dsh web: http://127.0.0.1:3099/?token=xxxx
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc string
		send := func(line string) {
			if m := tokenRe.FindStringSubmatch(line); m != nil {
				select {
				case got <- m[1]:
				default:
				}
			}
		}
		for {
			n, err := out.Read(buf)
			if n > 0 {
				// 按行切分，保留没有换行结尾的尾巴：一次 Read 未必读全一行，
				// 早先在单块里匹配 token，行被拆开就再也抓不到，最后 90 秒超时。
				acc += string(buf[:n])
				for {
					i := strings.IndexByte(acc, '\n')
					if i < 0 {
						break
					}
					line := strings.TrimRight(acc[:i], "\r")
					acc = acc[i+1:]
					if line != "" {
						r.addLog(line)
						send(line)
					}
				}
				if acc != "" {
					send(acc)
				}
			}
			if err != nil {
				return
			}
		}
	}()

	select {
	case tk := <-got:
		r.mu.Lock()
		r.token, r.started, r.phase = tk, true, PhaseRunning
		r.mu.Unlock()
		r.addLog(fmt.Sprintf("DSH 已启动：127.0.0.1:%d", port))
		return nil
	case <-time.After(90 * time.Second):
		r.Stop()
		r.fail("DSH 启动超时（90 秒没有打印地址）")
		return fmt.Errorf("DSH 启动超时")
	case <-ctx.Done():
		r.Stop()
		return ctx.Err()
	}
}

// Stop 停止 DSH 子进程。
func (r *Runtime) Stop() {
	r.mu.Lock()
	cmd := r.cmd
	was := r.started
	r.cmd, r.started, r.token = nil, false, ""
	if r.phase == PhaseRunning || r.phase == PhaseStarting {
		r.phase = PhaseStopped
	}
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		killTree(cmd.Process)
		// DSH 不一定马上响应 TERM；这里必须有超时再强杀，
		// 否则「停止」按钮会把整个请求挂死（实测会卡住不返回）。
		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
			r.addLog("DSH 没有响应停止信号，已强制结束")
		}
	}
	if was {
		r.addLog("DSH 已停止")
	}
}

// Running 判断子进程是否还活着。
func (r *Runtime) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// AddPlugin 把 npm 包装进 web profile。
//
// force=false 时，DSH 的版本守卫会拒掉不兼容的插件并返回 IncompatibleError；
// 调用方确认「仍然安装」后再用 force=true 重试。
func (r *Runtime) AddPlugin(ctx context.Context, pkg string, force bool) error {
	return r.pluginCmd(ctx, "add", pkg, force)
}

// RemovePlugin 从 web profile 卸掉插件。
func (r *Runtime) RemovePlugin(ctx context.Context, pkg string) error {
	return r.pluginCmd(ctx, "remove", pkg, false)
}

// IncompatibleError 表示 DSH 的版本守卫拒绝了这次安装。
//
// 带上解析出来的包与运行时版本，界面才能给出「仍然安装」的按钮，
// 而不是甩一句 exit status 1 让老师猜。
type IncompatibleError struct {
	Pkg    string
	Want   string // 插件要求的 DSH 版本（插件自报）
	Have   string // 当前运行时版本
	Detail string
}

func (e *IncompatibleError) Error() string {
	return fmt.Sprintf("%s 与当前 DSH %s 不兼容（插件要求 %s）", e.Pkg, e.Have, e.Want)
}

var incompatibleRe = regexp.MustCompile(`Plugin (\S+) is incompatible with dsh (\S+?):`)

func (r *Runtime) pluginCmd(ctx context.Context, action, pkg string, force bool) error {
	if !r.Installed() {
		return fmt.Errorf("还没安装 DSH 运行时，装好之后才能管理插件")
	}
	pkg = strings.TrimSpace(pkg)
	if pkg == "" || strings.ContainsAny(pkg, " \t\n;&|`$") {
		return fmt.Errorf("插件包名不合法")
	}
	node, _, err := r.probeNode()
	if err != nil {
		return err
	}
	r.addLog("dsh plugin " + action + " " + pkg)
	cmd := exec.CommandContext(ctx, node, r.entry(), "plugin", "--profile", "web", action, pkg)
	cmd.Dir = filepath.Join(r.root, "runtime")
	path := filepath.Dir(node)
	if runtime.GOOS == "windows" {
		path = filepath.Join(r.root, "node") + string(os.PathListSeparator) + path
	}
	cmd.Env = append(os.Environ(),
		"DSH_HOME="+filepath.Join(r.root, "home"),
		"PATH="+path+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CI=1",
	)
	var mu sync.Mutex
	var out strings.Builder
	cmd, done := pipeTo(cmd, func(line string) {
		r.addLog(line)
		mu.Lock()
		out.WriteString(line)
		out.WriteByte('\n')
		mu.Unlock()
	})
	hideWindow(cmd)
	runErr := cmd.Run()
	done()
	if runErr != nil {
		mu.Lock()
		text := out.String()
		mu.Unlock()
		if m := incompatibleRe.FindStringSubmatch(text); m != nil && action == "add" {
			ie := &IncompatibleError{Pkg: m[1], Have: m[2], Want: firstRequirement(text), Detail: text}
			if !force {
				return ie
			}
			// 用户已确认自担风险：先给这一版开豁免，再重试一次。
			r.addLog("忽略版本检查：" + ie.Pkg)
			if err := r.allowVersion(ctx, ie); err != nil {
				return fmt.Errorf("开启版本豁免失败：%w", err)
			}
			if _, err := r.runPluginOnce(ctx, "add", pkg); err != nil {
				return fmt.Errorf("插件安装仍然失败：%w", err)
			}
			return r.restartIfRunning(ctx)
		}
		return fmt.Errorf("dsh plugin %s 失败：%w", action, runErr)
	}
	return r.restartIfRunning(ctx)
}

// restartIfRunning 让插件变更立刻生效。
//
// 必须同步重启：早先写成后台 goroutine，结果是它和调用方的 Stop 抢进程，
// 留下一个占着端口的孤儿。
func (r *Runtime) restartIfRunning(ctx context.Context) error {
	if !r.Running() {
		return nil
	}
	r.Stop()
	if err := r.Start(ctx); err != nil {
		return fmt.Errorf("插件已装上，但重启 DSH 失败：%w", err)
	}
	return nil
}

// allowVersion 给某个插件的确切版本开豁免。
func (r *Runtime) allowVersion(ctx context.Context, ie *IncompatibleError) error {
	_, err := r.runPluginOnce(ctx, "allow-version", ie.Pkg, "--dsh-version", ie.Have, "--accept-risk")
	return err
}

// runPluginOnce 执行一次 dsh plugin 子命令，返回累积输出。
func (r *Runtime) runPluginOnce(ctx context.Context, args ...string) (string, error) {
	node, _, err := r.probeNode()
	if err != nil {
		return "", err
	}
	full := append([]string{r.entry(), "plugin", "--profile", "web"}, args...)
	cmd := exec.CommandContext(ctx, node, full...)
	cmd.Dir = filepath.Join(r.root, "runtime")
	path := filepath.Dir(node)
	if runtime.GOOS == "windows" {
		path = filepath.Join(r.root, "node") + string(os.PathListSeparator) + path
	}
	cmd.Env = append(os.Environ(),
		"DSH_HOME="+filepath.Join(r.root, "home"),
		"PATH="+path+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CI=1",
	)
	var mu sync.Mutex
	var out strings.Builder
	cmd, done := pipeTo(cmd, func(line string) {
		r.addLog(line)
		mu.Lock()
		out.WriteString(line)
		out.WriteByte('\n')
		mu.Unlock()
	})
	hideWindow(cmd)
	runErr := cmd.Run()
	done()
	mu.Lock()
	defer mu.Unlock()
	return out.String(), runErr
}

var requirementRe = regexp.MustCompile(`requirements (\{[^}]*\})`)

// firstRequirement 从拒绝信息里摘出插件自报的版本要求。
//
// DSH 的原文是
//
//	... is incompatible with dsh 0.2.1-rc.5: requirements {"@deepseek-ai/dsh-llm":"^0.1.0"}. Running it ...
//
// 后面还跟着一大段英文建议；只取第一对花括号，界面才好看得懂。
func firstRequirement(text string) string {
	if m := requirementRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func download(ctx context.Context, url, dst string, log func(string)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	cl := &http.Client{Timeout: 30 * time.Minute}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	total := resp.ContentLength
	pw := &progressWriter{total: total, log: log}
	_, err = io.Copy(io.MultiWriter(f, pw), resp.Body)
	return err
}

// progressWriter 每 10% 打一行，避免日志刷屏。
type progressWriter struct {
	n, total int64
	last     int64
	log      func(string)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if p.total > 0 {
		pct := p.n * 100 / p.total
		if pct/10 != p.last/10 {
			p.last = pct
			p.log(fmt.Sprintf("  %d%%（%.1f/%.1f MB）", pct, float64(p.n)/1048576, float64(p.total)/1048576))
		}
	}
	return len(b), nil
}

// unzipStrip 解压 zip 到 dst，并去掉一层固定前缀。
func unzipStrip(src, dst, prefix string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, prefix)
		if name == "" {
			continue
		}
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			continue // 防 zip slip
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode&0o111 == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// pipeTo 把 stdout/stderr 按行送到 log，返回 Run 之后必须调用的收尾函数。
//
// os/exec 不会替我们关闭任意 io.Writer，所以关管道这一步必须显式做，
// 否则日志会丢尾巴、读协程也会一直挂着。
func pipeTo(cmd *exec.Cmd, log func(string)) (*exec.Cmd, func()) {
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	go func() {
		buf := make([]byte, 8192)
		var acc string
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				acc += string(buf[:n])
				for {
					i := strings.IndexAny(acc, "\r\n")
					if i < 0 {
						break
					}
					if line := acc[:i]; strings.TrimSpace(line) != "" {
						log(line)
					}
					acc = acc[i+1:]
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return cmd, func() {
		_ = pw.Close()
		// 给读协程一点时间把缓冲区里的最后几行处理完。
		time.Sleep(50 * time.Millisecond)
	}
}

// writeLicense 在运行时节目录里留一份 DSH 的 MIT 许可与来源说明。
func (r *Runtime) writeLicense() {
	dir := filepath.Join(r.root, "runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "LICENSE-DeepSeek-Harness.txt"), []byte(dshLicense), 0o644)
	notice := `本目录由 ClassNote 安装，内含 DeepSeek Harness 运行时。

DeepSeek Harness（@deepseek-ai/dsh）
  https://github.com/deepseek-ai/deepseek-harness
  MIT License，版权归 DeepSeek，全文见同目录 LICENSE-DeepSeek-Harness.txt

ClassNote 只负责在你自己的电脑上安装并启动它，不分发、不修改其源代码。
插件由各自作者以各自许可发布，安装前请自行确认来源可信。
`
	_ = os.WriteFile(filepath.Join(dir, "README-ClassNote.txt"), []byte(notice), 0o644)
}
