package asrmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"classnote/internal/logx"
)

// Model 描述一个可下载的 whisper.cpp ggml 模型。
//
// 只收录量化版（q5_1 / q5_0）：fp16 原版体积翻 2-3 倍，而课堂上
// 省下的等待时间比那 1% 的准确率更值钱。体积与 sha256 取自
// huggingface.co/ggerganov/whisper.cpp，校验不过会自动重下一次。
type Model struct {
	ID      string `json:"id"`
	File    string `json:"file"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Quality int    `json:"quality"` // 1-5，中文准确率
	Speed   int    `json:"speed"`   // 1-5，同机器上的相对速度
	Note    string `json:"note"`
	Tag     string `json:"tag"` // 轻量 / 推荐 / 高精度 / 极速
}

// Models 是内置模型清单，按体积从小到大。
//
// 默认自动部署 base_q5_1（59.7 MB）：tiny 对中文几乎不可用，
// small 的 190 MB 在首次启动时等太久，59 MB 是「听得懂」与
// 「等得起」的平衡点。想更准的老师可以在设置里一键换。
var Models = []Model{
	{
		ID: "tiny_q5_1", File: "ggml-tiny-q5_1.bin", Name: "Tiny", Size: 32152673,
		SHA256:  "818710568da3ca15689e31a743197b520007872ff9576237bda97bd1b469c3d7",
		Quality: 1, Speed: 5, Tag: "极速",
		Note: "体积最小、速度最快，中文只能听个大概。仅在很老的机器上作为兜底。",
	},
	{
		ID: "base_q5_1", File: "ggml-base-q5_1.bin", Name: "Base", Size: 59707625,
		SHA256:  "422f1ae452ade6f30a004d7e5c6a43195e4433bc370bf23fac9cc591f01a8898",
		Quality: 2, Speed: 4, Tag: "轻量",
		Note: "默认自动部署。日常讲课能出可读的句子，专业术语需要课后校对。",
	},
	{
		ID: "small_q5_1", File: "ggml-small-q5_1.bin", Name: "Small", Size: 190085487,
		SHA256:  "ae85e4a935d7a567bd102fe55afc16bb595bdb618e11b2fc7591bc08120411bb",
		Quality: 3, Speed: 3, Tag: "推荐",
		Note: "中文准确率明显好于 Base，8 代 i5 以上都能实时跟上。多数教室的最优解。",
	},
	{
		ID: "medium_q5_0", File: "ggml-medium-q5_0.bin", Name: "Medium", Size: 539212467,
		SHA256:  "19fea4b380c3a618ec4723c3eef2eb785ffba0d0538cf43f8f235e7b3b34220f",
		Quality: 4, Speed: 2, Tag: "高精度",
		Note: "术语与长句更稳，但低功耗笔记本上会滞后几秒。适合课后整段重转。",
	},
	{
		ID: "large_v3_turbo_q5_0", File: "ggml-large-v3-turbo-q5_0.bin", Name: "Large v3 Turbo", Size: 574041195,
		SHA256:  "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2",
		Quality: 5, Speed: 3, Tag: "最佳中文",
		Note: "whisper 系列中文最好的版本，速度接近 Medium。有独显或新 CPU 时首选。",
	},
}

// DefaultModelID 是首次运行自动部署的模型。
const DefaultModelID = "base_q5_1"

// 下载源。国内优先走 hf-mirror，海外直连 huggingface。
//
// 顺序有意义：先试国内镜像，失败再回落官方，最后才用用户自定义。
var defaultSources = []string{
	"https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/",
	"https://huggingface.co/ggerganov/whisper.cpp/resolve/main/",
}

// FindModel 按 ID 查清单。
func FindModel(id string) (Model, bool) {
	for _, m := range Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// FindModelByFile 按文件名反查，用于把配置里的路径映射回清单。
func FindModelByFile(base string) (Model, bool) {
	for _, m := range Models {
		if m.File == base {
			return m, true
		}
	}
	return Model{}, false
}

// ---- 运行时目录 ----

var (
	dirMu      sync.RWMutex
	runtimeDir string
)

// SetRuntimeDir 记录 whisper 运行时的实际目录（可能是 %ProgramData% 下的副本）。
//
// 模型必须下到这个目录的 models 子目录里，否则 whisper-server 找不到；
// 而运行时目录取决于安装路径是否含中文，只有 main 知道，所以由它注入。
func SetRuntimeDir(dir string) {
	dirMu.Lock()
	runtimeDir = dir
	dirMu.Unlock()
}

// RuntimeDir 返回运行时目录。
func RuntimeDir() string {
	dirMu.RLock()
	defer dirMu.RUnlock()
	return runtimeDir
}

// modelsDir 返回模型存放目录。
func modelsDir() string {
	d := RuntimeDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "models")
}

// LocalModelPath 返回某个模型在本机的落地路径。
func LocalModelPath(m Model) string {
	d := modelsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, m.File)
}

// HasModel 判断模型是否已经下载完整。
func HasModel(m Model) bool {
	p := LocalModelPath(m)
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Size() == m.Size
}

// ---- 进度与任务 ----

// Progress 是一次下载的可观测状态。
type Progress struct {
	ID      string `json:"id"`
	State   string `json:"state"` // idle | downloading | verifying | done | error | canceled
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Speed   int64  `json:"speed"` // 字节/秒
	Source  string `json:"source"`
	Error   string `json:"error"`
	Elapsed int64  `json:"elapsedMs"`
}

// Manager 管理模型下载。同一时刻只跑一个任务：
// 并发下载只会互相抢带宽，反而更慢，界面也没法说清进度。
type Manager struct {
	mu     sync.Mutex
	task   *dlTask
	notify func(Progress)
}

type dlTask struct {
	id     string
	cancel context.CancelFunc
	prog   Progress
	start  time.Time
}

// NewManager 创建下载管理器。notify 会被高频调用，调用方自行节流。
func NewManager(notify func(Progress)) *Manager {
	return &Manager{notify: notify}
}

// Current 返回当前任务的进度；空闲时 State 为 idle。
func (m *Manager) Current() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.task == nil {
		return Progress{State: "idle"}
	}
	p := m.task.prog
	p.Elapsed = time.Since(m.task.start).Milliseconds()
	return p
}

// Busy 判断是否有下载在跑。
//
// 任务完成后不清空 task：界面要能显示「上次失败的原因」或
// 「刚刚装好」，清空就只剩一句 idle，老师不知道该干嘛。
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.task == nil {
		return false
	}
	s := m.task.prog.State
	return s == "downloading" || s == "verifying"
}

// Cancel 取消当前下载（保留 .part 供下次续传）。
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	t := m.task
	m.mu.Unlock()
	if t == nil {
		return
	}
	if id != "" && t.id != id {
		return
	}
	t.cancel()
}

// Start 启动下载。mirror 非空时优先用它。
func (m *Manager) Start(id, mirror string) error {
	mm, ok := FindModel(id)
	if !ok {
		return fmt.Errorf("未知的模型：%s", id)
	}
	if HasModel(mm) {
		return fmt.Errorf("%s 已经下载好了", mm.Name)
	}
	dir := modelsDir()
	if dir == "" {
		return fmt.Errorf("未找到语音运行时目录，请先确认程序目录完整")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建模型目录失败: %w", err)
	}

	m.mu.Lock()
	if m.task != nil {
		s := m.task.prog.State
		if s == "downloading" || s == "verifying" {
			id := m.task.id
			m.mu.Unlock()
			return fmt.Errorf("已有下载在进行（%s），请等它结束或先取消", id)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.task = &dlTask{
		id:     id,
		cancel: cancel,
		start:  time.Now(),
		prog:   Progress{ID: id, State: "downloading", Total: mm.Size},
	}
	m.mu.Unlock()

	go m.run(ctx, mm, dir, mirror)
	return nil
}

func (m *Manager) update(fn func(p *Progress)) {
	m.mu.Lock()
	if m.task != nil {
		fn(&m.task.prog)
	}
	p := Progress{State: "idle"}
	if m.task != nil {
		p = m.task.prog
		p.Elapsed = time.Since(m.task.start).Milliseconds()
	}
	n := m.notify
	m.mu.Unlock()
	if n != nil {
		n(p)
	}
}

func (m *Manager) finish(state, errMsg string) {
	m.mu.Lock()
	if m.task != nil {
		m.task.prog.State = state
		m.task.prog.Error = errMsg
	}
	p := Progress{State: "idle"}
	if m.task != nil {
		p = m.task.prog
		p.Elapsed = time.Since(m.task.start).Milliseconds()
	}
	n := m.notify
	m.mu.Unlock()
	if n != nil {
		n(p)
	}
}

func (m *Manager) run(ctx context.Context, mm Model, dir, mirror string) {
	dst := filepath.Join(dir, mm.File)
	part := dst + ".part"

	// 断点续传：上次留下的 .part 从它的末尾接着下。
	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
		if offset >= mm.Size {
			// 尺寸已经够了，只是上次没来得及改名。
			if err := verifyAndRename(part, dst, mm); err == nil {
				m.finish("done", "")
				m.autoActivate(mm)
				return
			}
			offset = 0
			_ = os.Remove(part)
		}
	}

	if err := m.download(ctx, mm, part, offset, mirror); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			m.finish("canceled", "")
		default:
			m.finish("error", err.Error())
		}
		return
	}

	m.update(func(p *Progress) { p.State = "verifying" })
	keepPart := false
	err := verifyAndRename(part, dst, mm)
	if err != nil && ctx.Err() == nil {
		// 校验失败多半是文件被写坏或上游更新了。删掉重下一次，
		// 还不行才报错——不然老师只会看到一句「失败了」。
		logx.Warnf("asr", "模型校验失败，重新下载：%v", err)
		_ = os.Remove(part)
		m.update(func(p *Progress) { p.Done = 0 })
		if err2 := m.download(ctx, mm, part, 0, mirror); err2 == nil {
			err = verifyAndRename(part, dst, mm)
			keepPart = err != nil
		} else {
			err = err2
		}
	}
	if err != nil {
		if !keepPart {
			_ = os.Remove(part)
		}
		if ctx.Err() != nil {
			m.finish("canceled", "")
			return
		}
		m.finish("error", err.Error())
		return
	}
	m.finish("done", "")
	logx.Infof("asr", "语音模型已就绪：%s（%.1f MB）", mm.File, float64(mm.Size)/(1<<20))
	m.autoActivate(mm)
}

// download 把一个文件下到 part，支持续传、多源回落、限频进度。
func (m *Manager) download(ctx context.Context, mm Model, part string, offset int64, mirror string) error {
	var lastErr error
	for _, src := range sourceList(mirror) {
		// 同一个源试两次：镜像偶发不响应很常见，重试往往就好了，
		// 直接判死会让老师看到一次莫名其妙的失败。
		for attempt := 1; attempt <= 2; attempt++ {
			if ctx.Err() != nil {
				return context.Canceled
			}
			err := m.fetch(ctx, src+mm.File, part, offset, mm.Size)
			if err == nil {
				return nil
			}
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return context.Canceled
			}
			lastErr = err
			logx.Warnf("asr", "从 %s 下载失败（第 %d 次）：%v", hostOf(src), attempt, err)
			m.update(func(p *Progress) { p.Source = "" })
			// 续传点可能变了，重新取一次。
			if fi, e := os.Stat(part); e == nil {
				offset = fi.Size()
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的下载源")
	}
	return fmt.Errorf("所有下载源都失败了：%w", lastErr)
}

func sourceList(mirror string) []string {
	out := make([]string, 0, 3)
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		if !strings.HasSuffix(s, "/") {
			s += "/"
		}
		return s
	}
	if v := clean(mirror); v != "" {
		out = append(out, v)
	}
	for _, s := range defaultSources {
		if v := clean(s); v != "" && v != clean(mirror) {
			out = append(out, v)
		}
	}
	return out
}

func hostOf(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	return u
}

// stallTimeout 是「多久没收到数据就换源」。
//
// 不设成 10 秒：慢网下 TCP 窗口收缩会让两次读到数据之间隔几十秒，
// 误判成卡死反而更糟。45 秒足够区分「慢」和「僵」。
const stallTimeout = 45 * time.Second

// dlClient 不设整体超时：574 MB 的模型在慢网下要十几分钟。
var dlClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	},
}

func (m *Manager) fetch(ctx context.Context, url, part string, offset, total int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ClassNote/model-downloader")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// 服务端不支持 Range：只能从头来。
		offset = 0
	case http.StatusPartialContent:
		// 正常续传。
	case http.StatusRequestedRangeNotSatisfiable:
		// .part 比远端还长，说明本地文件坏了。
		_ = os.Remove(part)
		return fmt.Errorf("续传位置无效，已清除缓存重试")
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	m.update(func(p *Progress) {
		p.Done = offset
		p.Total = total
		p.Source = hostOf(url)
	})

	// 无进展看门狗：连续 stallTimeout 收不到任何字节就放弃这个源。
	// 没有它，网络半断的时候界面会永远停在 0%，老师只能以为程序死了。
	reqCtx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()
	var lastAct atomic.Int64
	lastAct.Store(time.Now().UnixNano())
	go func() {
		tk := time.NewTicker(3 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-reqCtx.Done():
				return
			case <-tk.C:
				if time.Since(time.Unix(0, lastAct.Load())) > stallTimeout {
					cancelReq()
					return
				}
			}
		}
	}()
	_ = reqCtx

	buf := make([]byte, 256<<10)
	done := offset
	lastEmit := time.Now()
	lastDone := done
	var speed int64
	for {
		if ctx.Err() != nil {
			return context.Canceled
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写入磁盘失败（空间不足？）: %w", werr)
			}
			done += int64(n)
			lastAct.Store(time.Now().UnixNano())
			if now := time.Now(); now.Sub(lastEmit) >= 400*time.Millisecond {
				if dt := now.Sub(lastEmit).Seconds(); dt > 0 {
					speed = int64(float64(done-lastDone) / dt)
				}
				lastEmit, lastDone = now, done
				m.update(func(p *Progress) { p.Done, p.Speed, p.Total = done, speed, total })
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return context.Canceled
			}
			if time.Since(time.Unix(0, lastAct.Load())) > stallTimeout {
				return fmt.Errorf("连接卡住超过 %s，已放弃这个源", stallTimeout)
			}
			return rerr
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	m.update(func(p *Progress) { p.Done, p.Total = done, total })
	if total > 0 && done != total {
		return fmt.Errorf("下载不完整（%d / %d 字节）", done, total)
	}
	return nil
}

// verifyAndRename 校验大小与 sha256，通过后改成正式文件名。
func verifyAndRename(part, dst string, mm Model) error {
	fi, err := os.Stat(part)
	if err != nil {
		return fmt.Errorf("下载文件不存在: %w", err)
	}
	if fi.Size() != mm.Size {
		return fmt.Errorf("文件大小不符（%d，应为 %d）", fi.Size(), mm.Size)
	}
	if mm.SHA256 != "" {
		sum, err := fileSHA256(part)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, mm.SHA256) {
			return fmt.Errorf("sha256 校验不通过")
		}
	}
	return os.Rename(part, dst)
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// autoActivate 下载完成后直接启用，省得老师还要多点一次。
func (m *Manager) autoActivate(mm Model) {
	if onModelReady != nil {
		onModelReady(mm)
	}
}

var onModelReady func(Model)

// OnModelReady 注册「模型下载完成」的回调，由服务端接管（启动服务 + 写配置）。
func OnModelReady(fn func(Model)) { onModelReady = fn }

// ---- 状态快照 ----

// ModelStatus 是清单里某一项的本地状态。
type ModelStatus struct {
	Model
	Installed bool  `json:"installed"`
	Partial   int64 `json:"partial"` // 已有 .part 的字节数，0 表示没有
	Active    bool  `json:"active"`
}

// Status 汇总当前运行时与全部模型的状态。
type Status struct {
	Dir      string        `json:"dir"`
	Ready    bool          `json:"ready"` // whisper-server 是否在跑
	Port     int           `json:"port"`
	Active   string        `json:"active"` // 当前使用的模型 ID
	Models   []ModelStatus `json:"models"`
	Task     Progress      `json:"task"`
	Platform string        `json:"platform"`
	Sources  []string      `json:"sources"`
}

// Snap 生成状态快照。ready 由调用方探测后传入，本包不碰进程。
func (m *Manager) Snap(port int, activePath string, ready bool) Status {
	st := Status{
		Dir:      RuntimeDir(),
		Port:     port,
		Platform: runtime.GOOS,
		Task:     m.Current(),
		Sources:  sourceList(""),
	}
	st.Ready = ready
	activeFile := filepath.Base(activePath)
	for _, mm := range Models {
		ms := ModelStatus{Model: mm, Installed: HasModel(mm)}
		if p := LocalModelPath(mm); p != "" {
			if fi, err := os.Stat(p + ".part"); err == nil {
				ms.Partial = fi.Size()
			}
		}
		if ms.Installed && activeFile != "" && activeFile == mm.File {
			ms.Active = true
			st.Active = mm.ID
		}
		st.Models = append(st.Models, ms)
	}
	return st
}

// MarshalJSON 保证 Progress 在没有任务时也能序列化成完整的 idle 对象。
func (p Progress) MarshalJSON() ([]byte, error) {
	type alias Progress
	if p.State == "" {
		p.State = "idle"
	}
	return json.Marshal(alias(p))
}
