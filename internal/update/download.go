package update

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"classnote/internal/logx"
)

// Endpoint 是下载器实际会去请求的一条地址（已按测速排好序）。
type Endpoint struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Progress 是一次更新的可观测状态。
type Progress struct {
	State   string `json:"state"` // idle | probing | downloading | verifying | done | error | canceled
	Name    string `json:"name"`
	Node    string `json:"node"`
	NodeID  string `json:"nodeId"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Speed   int64  `json:"speed"` // 字节/秒
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
	Tried   int    `json:"tried"`
	Elapsed int64  `json:"elapsedMs"`
}

// Job 是一次下载任务。
type Job struct {
	URL       string     // 原始 GitHub 地址（写日志用）
	Name      string     // 落地文件名
	Dir       string     // 落地目录
	Token     string     // 私有仓库令牌，只发给 GitHub 自有域
	Expect    int64      // 期望体积（0 表示未知）
	Endpoints []Endpoint // 已排序的下载地址；为空时下载器自己测速
	Custom    []string   // 自定义节点前缀（Endpoints 为空时用来测速）
}

// Downloader 管理更新包下载。同一时刻只跑一个任务：
// 两个任务并发只会互相抢带宽，界面也没法说清进度。
type Downloader struct {
	mu     sync.Mutex
	task   *dlTask
	notify func(Progress)
}

type dlTask struct {
	cancel context.CancelFunc
	job    Job
	prog   Progress
	start  time.Time
}

// NewDownloader 创建下载器。notify 会被高频调用，调用方自行节流。
func NewDownloader(notify func(Progress)) *Downloader {
	return &Downloader{notify: notify}
}

// Current 返回当前任务进度；空闲时 State 为 idle。
func (d *Downloader) Current() Progress {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current()
}

func (d *Downloader) current() Progress {
	if d.task == nil {
		return Progress{State: "idle"}
	}
	p := d.task.prog
	p.Elapsed = time.Since(d.task.start).Milliseconds()
	return p
}

// Busy 判断是否有任务在跑。
//
// 完成后不清空 task：界面要能继续显示「刚下好」或失败原因，
// 清空就只剩一句 idle，老师不知道下一步该点哪。
func (d *Downloader) Busy() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.task == nil {
		return false
	}
	s := d.task.prog.State
	return s == "probing" || s == "downloading" || s == "verifying"
}

// Cancel 取消当前下载（保留 .part 供下次续传）。
func (d *Downloader) Cancel() {
	d.mu.Lock()
	t := d.task
	d.mu.Unlock()
	if t != nil && t.cancel != nil {
		t.cancel()
	}
}

// Start 启动下载。
func (d *Downloader) Start(job Job) error {
	if job.Name == "" {
		job.Name = filepath.Base(strings.SplitN(job.URL, "?", 2)[0])
	}
	// 只取最后一段并去掉 ..，否则 name 传 "../../x.exe" 就能写到程序目录之外。
	job.Name = filepath.Base(filepath.Clean("/" + job.Name))
	if job.Name == "" || job.Name == "." || job.Name == ".." || job.Name == string(filepath.Separator) {
		return fmt.Errorf("更新包文件名不合法")
	}
	if job.URL == "" {
		return fmt.Errorf("下载地址为空")
	}
	u, err := url.Parse(job.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("下载地址不合法")
	}
	if err := os.MkdirAll(job.Dir, 0o755); err != nil {
		return fmt.Errorf("创建下载目录失败: %w", err)
	}

	d.mu.Lock()
	if d.task != nil {
		s := d.task.prog.State
		if s == "probing" || s == "downloading" || s == "verifying" {
			name := d.task.prog.Name
			d.mu.Unlock()
			return fmt.Errorf("已有下载在进行（%s），请等它结束或先取消", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.task = &dlTask{
		cancel: cancel,
		job:    job,
		start:  time.Now(),
		prog: Progress{
			State: "probing", Name: job.Name, Total: job.Expect,
			Node: "正在测速选节点…",
		},
	}
	d.mu.Unlock()

	go d.run(ctx, job)
	return nil
}

func (d *Downloader) update(fn func(p *Progress)) {
	d.mu.Lock()
	if d.task != nil {
		fn(&d.task.prog)
	}
	p := d.current()
	n := d.notify
	d.mu.Unlock()
	if n != nil {
		n(p)
	}
}

func (d *Downloader) finish(state, msg string) {
	d.mu.Lock()
	if d.task != nil {
		d.task.prog.State = state
		d.task.prog.Error = msg
	}
	p := d.current()
	n := d.notify
	d.mu.Unlock()
	if n != nil {
		n(p)
	}
}

// stallTimeout 是「多久没收到数据就换节点」。
//
// 不设成 10 秒：慢网下 TCP 窗口收缩会让两次读到数据之间隔几十秒，
// 误判成卡死反而更糟。30 秒足够区分「慢」和「僵」。
const stallTimeout = 30 * time.Second

// dlClient 不设整体超时：80 MB 的包在慢网下要十几分钟。
var dlClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 25 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
	},
}

func (d *Downloader) run(ctx context.Context, job Job) {
	dst := filepath.Join(job.Dir, job.Name)
	part := dst + ".part"

	eps := job.Endpoints
	if len(eps) == 0 {
		nodes := TestNodes(ctx, ProbeReq{URL: job.URL, Custom: job.Custom, DirectOnly: job.Token != ""})
		eps = Endpoints(job.URL, nodes)
		if best, ok := firstOK(nodes); ok {
			d.update(func(p *Progress) { p.Node, p.NodeID = best.Name, best.ID })
		}
		if len(eps) == 0 {
			d.finish("error", "没有可用的下载节点")
			return
		}
	}
	if ctx.Err() != nil {
		d.finish("canceled", "")
		return
	}
	d.update(func(p *Progress) { p.State = "downloading" })

	// 断点续传：上次留下的 .part 从它的末尾接着下。
	offset := resumeOffset(part, job.Expect)
	if err := d.tryEndpoints(ctx, eps, part, offset, job, dst); err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			d.finish("canceled", "")
			return
		}
		d.finish("error", "所有下载节点都失败了："+err.Error())
		return
	}

	d.update(func(p *Progress) { p.State = "verifying" })
	// 大小对不上：多半是某个节点返回了截断的响应。
	if fi, err := os.Stat(part); err == nil && job.Expect > 0 && fi.Size() != job.Expect {
		_ = os.Remove(part)
		d.update(func(p *Progress) { p.Done = 0 })
		if err := d.retryOnce(ctx, eps, part, job, dst); err != nil {
			d.finish("error", err.Error())
			return
		}
	}
	if err := verifyZip(part); err != nil {
		_ = os.Remove(part)
		d.update(func(p *Progress) { p.Done = 0 })
		if err2 := d.retryOnce(ctx, eps, part, job, dst); err2 != nil {
			d.finish("error", err2.Error())
			return
		}
		if err2 := verifyZip(part); err2 != nil {
			_ = os.Remove(part)
			d.finish("error", "下载的更新包不是有效的 zip："+err2.Error())
			return
		}
	}
	// Windows 上 os.Rename 不覆盖已存在的目标：同名旧包要先删掉。
	_ = os.Remove(dst)
	if err := os.Rename(part, dst); err != nil {
		d.finish("error", "保存更新包失败："+err.Error())
		return
	}
	fi, _ := os.Stat(dst)
	size := int64(0)
	if fi != nil {
		size = fi.Size()
	}
	d.mu.Lock()
	if d.task != nil {
		d.task.prog.Path = dst
		d.task.prog.Done, d.task.prog.Total = size, size
	}
	d.mu.Unlock()
	d.finish("done", "")
	logx.Infof("update", "更新包已下载：%s（%.1f MB）", dst, float64(size)/(1<<20))
}

// resumeOffset 计算续传起点，缓存明显不对时直接清掉。
func resumeOffset(part string, expect int64) int64 {
	fi, err := os.Stat(part)
	if err != nil {
		return 0
	}
	if expect > 0 && fi.Size() > expect {
		_ = os.Remove(part) // 缓存比远端还大，只能是坏的
		return 0
	}
	return fi.Size()
}

// tryEndpoints 依次尝试所有节点，两轮：第二轮是给「偶发 5xx」的第二次机会。
func (d *Downloader) tryEndpoints(ctx context.Context, eps []Endpoint, part string, offset int64, job Job, dst string) error {
	var lastErr error
	for pass := 0; pass < 2; pass++ {
		for _, ep := range eps {
			if ctx.Err() != nil {
				return context.Canceled
			}
			d.update(func(p *Progress) {
				p.Node, p.NodeID = ep.Name, ep.ID
				p.Tried++
			})
			err := d.fetch(ctx, ep.URL, part, offset, job, dst)
			if err == nil {
				return nil
			}
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return context.Canceled
			}
			lastErr = err
			logx.Warnf("update", "从 %s 下载失败（第 %d 轮）：%v", ep.Name, pass+1, err)
			// 续传点可能变了，重新取一次。
			offset = resumeOffset(part, job.Expect)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的下载节点")
	}
	return lastErr
}

// retryOnce 在校验失败后从第一个节点重新下一遍。
//
// 校验失败基本只有两种原因：节点给了坏数据，或者本地磁盘写坏了。
// 两种都值得重来一次，而不是直接丢一个「更新失败」给老师。
func (d *Downloader) retryOnce(ctx context.Context, eps []Endpoint, part string, job Job, dst string) error {
	_ = os.Remove(part)
	var lastErr error
	for _, ep := range eps {
		if ctx.Err() != nil {
			return context.Canceled
		}
		d.update(func(p *Progress) { p.Node, p.NodeID = ep.Name, ep.ID })
		if err := d.fetch(ctx, ep.URL, part, 0, job, dst); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的下载节点")
	}
	return fmt.Errorf("重新下载失败：%w", lastErr)
}

// fetch 把一个地址下到 part，支持续传、限频进度、无进展看门狗。
func (d *Downloader) fetch(ctx context.Context, rawURL, part string, offset int64, job Job, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ClassNote/updater")
	// Token 只发给 GitHub 自己的域：这条地址来自请求体或反代前缀，
	// 无条件带 Authorization 等于把凭据交给任意主机。
	if job.Token != "" && trustedHost(hostOf(rawURL)) {
		req.Header.Set("Authorization", "Bearer "+job.Token)
	}
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
		_ = os.Remove(part)
		return fmt.Errorf("续传位置无效，已清除缓存")
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
		return fmt.Errorf("写入磁盘失败（空间不足？）: %w", err)
	}
	defer f.Close()

	total := job.Expect
	if total == 0 {
		if n := totalFromRange(resp.Header.Get("Content-Range")); n > 0 {
			total = n
		} else if resp.ContentLength > 0 {
			total = resp.ContentLength + offset
		}
	}
	d.update(func(p *Progress) {
		p.Done, p.Total = offset, total
		p.Speed = 0
	})

	// 无进展看门狗：连续 stallTimeout 收不到任何字节就放弃这个节点。
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
				d.update(func(p *Progress) { p.Done, p.Speed, p.Total = done, speed, total })
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
				return fmt.Errorf("连接卡住超过 %s，已放弃这个节点", stallTimeout)
			}
			return rerr
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	d.update(func(p *Progress) { p.Done, p.Total, p.Speed = done, total, speed })
	if total > 0 && done != total {
		return fmt.Errorf("下载不完整（%d / %d 字节）", done, total)
	}
	return nil
}

// verifyZip 确认下载到的是一个能打开的 zip，并且里面有内容。
//
// 反代节点在限流或额度用尽时会返回一个 HTML 错误页，HTTP 状态却是 200；
// 不校验的话，把这种文件交给解压步骤，用户看到的是「解压失败」。
func verifyZip(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("不是有效的 zip（%v）", err)
	}
	defer r.Close()
	if len(r.File) == 0 {
		return fmt.Errorf("压缩包是空的")
	}
	return nil
}
