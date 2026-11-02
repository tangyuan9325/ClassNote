// Package lesson 是课堂助手的核心：编排录音、转写、翻页检测、AI 分析与报告。
package lesson

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"classnote/internal/asr"
	"classnote/internal/audio"
	"classnote/internal/capture"
	"classnote/internal/config"
	"classnote/internal/llm"
	"classnote/internal/logx"
	"classnote/internal/paths"
	"classnote/internal/store"
)

// Event 是推送给前端的实时事件。
type Event struct {
	Type string    `json:"type"`
	Data any       `json:"data"`
	At   time.Time `json:"at"`
}

// Status 描述引擎当前状态。
type Status struct {
	Running    bool           `json:"running"`
	SessionID  string         `json:"sessionId"`
	Subject    string         `json:"subject"`
	Title      string         `json:"title"`
	StartedAt  time.Time      `json:"startedAt"`
	ElapsedSec int            `json:"elapsedSec"`
	Stage      string         `json:"stage"` // idle | listening | analyzing | reporting
	Recording  bool           `json:"recording"`
	Speaking   bool           `json:"speaking"`
	Level      float64        `json:"level"`
	Segments   int            `json:"segments"`
	Words      int            `json:"words"`
	Slide      capture.Status `json:"slide"`
	AudioError string         `json:"audioError,omitempty"`
	AIError    string         `json:"aiError,omitempty"`
	ASR        string         `json:"asr"`
	Model      string         `json:"model"`
}

type asrJob struct {
	segID string
	wav   []byte
	at    time.Time
}

type segReq struct {
	fn    func(*audio.Segmenter) []audio.Segment
	reply chan []audio.Segment
}

// Engine 编排一整堂课。
type Engine struct {
	mu          sync.Mutex
	cfg         config.Config
	paths       paths.Paths
	store       *store.Store
	det         *capture.Detector
	client      llm.Client
	asrCli      *asr.Transcriber
	skillPrompt string
	lastSubject string
	emitFn      func(Event)

	sess      *store.Session
	rec       *audio.Recorder
	seg       *audio.Segmenter
	segCh     chan segReq
	cur       *store.Segment
	running   bool
	stage     string
	watchStop chan struct{}
	stopping  bool
	wgWork    sync.WaitGroup // 录音 + 翻页检测
	wgASR     sync.WaitGroup
	wgAna     sync.WaitGroup
	asrQ      chan asrJob
	anaQ      chan string
	pending   map[string]int
	level     float64
	speaking  bool
	audioErr  string
	aiErr     string
	lastSegAt time.Time
	captures  string
	startedAt time.Time
	deck      *capture.Deck
	deckPath  string
}

// New 创建引擎。
func New(paths paths.Paths, st *store.Store, cfg config.Config) *Engine {
	return &Engine{
		paths:   paths,
		store:   st,
		cfg:     cfg,
		det:     capture.NewDetector(cfg.Capture),
		asrCli:  asr.New(cfg.ASR),
		stage:   "idle",
		pending: map[string]int{},
	}
}

// OnEvent 注册事件回调。
func (e *Engine) OnEvent(fn func(Event)) { e.emitFn = fn }

// EmitExternal 允许外部模块（如课程监控）注入事件。
func (e *Engine) EmitExternal(t string, data any) { e.emit(t, data) }

func (e *Engine) emit(t string, data any) {
	if e.emitFn == nil {
		return
	}
	e.emitFn(Event{Type: t, Data: data, At: time.Now()})
}

// Update 热更新配置。
func (e *Engine) Update(cfg config.Config) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
	e.det.UpdateConfig(cfg.Capture)
	e.asrCli.Update(cfg.ASR)
}

// SetClient 更新模型客户端。
// SetSkillPrompt 注入启用中的技能包提示（每次会变化，直接覆盖）。
func (e *Engine) SetSkillPrompt(p string) {
	e.mu.Lock()
	e.skillPrompt = p
	e.mu.Unlock()
}

func (e *Engine) SetClient(c llm.Client) {
	e.mu.Lock()
	e.client = c
	e.mu.Unlock()
}

// Detector 返回截图检测器。
func (e *Engine) Detector() *capture.Detector { return e.det }

// Cfg 返回配置副本。
func (e *Engine) Cfg() config.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// Session 返回当前会话（可能为 nil）。
func (e *Engine) Session() *store.Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sess
}

// SessionCopy 返回深拷贝，供 HTTP 层安全序列化。
func (e *Engine) SessionCopy() *store.Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return copySession(e.sess)
}

func copySession(s *store.Session) *store.Session {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Segments = make([]*store.Segment, len(s.Segments))
	for i, seg := range s.Segments {
		sc := *seg
		cp.Segments[i] = &sc
	}
	cp.Messages = append([]store.ChatMessage(nil), s.Messages...)
	return &cp
}

// Status 返回状态快照。
func (e *Engine) Status() Status {
	e.mu.Lock()
	st := Status{
		Running: e.running, Stage: e.stage, Level: e.level, Speaking: e.speaking,
		AudioError: e.audioErr, AIError: e.aiErr, Recording: e.rec != nil,
		ASR: e.asrCli.Describe(),
	}
	if e.sess != nil {
		st.SessionID = e.sess.ID
		st.Subject = e.sess.Subject
		st.Title = e.sess.Title
		st.StartedAt = e.sess.StartedAt
		st.ElapsedSec = int(time.Since(e.sess.StartedAt).Seconds())
		st.Segments = len(e.sess.Segments)
		for _, s := range e.sess.Segments {
			st.Words += len([]rune(s.Transcript))
		}
	}
	if e.client != nil {
		st.Model = e.client.Describe()
	}
	e.mu.Unlock()
	st.Slide = e.det.Status()
	return st
}

// persist 在持锁状态下保存会话，避免并发写坏 JSON。
func (e *Engine) persist() {
	e.mu.Lock()
	sess := copySession(e.sess)
	e.mu.Unlock()
	if sess == nil {
		return
	}
	if err := e.store.Save(sess); err != nil {
		logx.Warnf("lesson", "保存会话失败: %v", err)
	}
}

// Start 开始一堂课。
func (e *Engine) Start(subject, title, source string) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return fmt.Errorf("已经在记录中")
	}
	now := time.Now()
	if subject == "" {
		subject = "课堂"
	}
	if title == "" {
		title = subject + " · " + now.Format("01-02 15:04")
	}
	sess := &store.Session{
		ID: store.NewID(now), Title: title, Subject: subject, Source: source,
		StartedAt: now, Status: "recording",
	}
	if e.client != nil {
		sess.AIModel = e.client.Describe()
	}
	e.sess = sess
	e.running = true
	e.stage = "listening"
	e.audioErr = ""
	e.aiErr = ""
	e.pending = map[string]int{}
	e.watchStop = make(chan struct{})
	e.asrQ = make(chan asrJob, 128)
	e.anaQ = make(chan string, 128)
	e.segCh = make(chan segReq, 8)
	e.captures = filepath.Join(e.paths.Captures, sess.ID)
	e.startedAt = now
	capDir := e.captures
	cfg := e.cfg
	e.mu.Unlock()

	if err := os.MkdirAll(capDir, 0o755); err != nil {
		logx.Warnf("lesson", "创建截图目录失败: %v", err)
	}
	e.det.Reset()

	first := e.newSegment(1, nil)
	e.mu.Lock()
	e.cur = first
	e.sess.Segments = append(e.sess.Segments, first)
	e.lastSegAt = now
	e.mu.Unlock()
	e.persist()

	// 录音（转写关闭时仍然记录，便于日后补转写）。
	rate := cfg.ASR.SampleRate
	if rate <= 0 {
		rate = 16000
	}
	rec := audio.New(rate)
	if err := rec.Start(cfg.ASR.Device); err != nil {
		logx.Warnf("lesson", "启动录音失败: %v", err)
		e.mu.Lock()
		e.audioErr = err.Error()
		e.mu.Unlock()
		e.emit("error", map[string]string{"scope": "audio", "message": err.Error()})
	} else {
		e.mu.Lock()
		e.rec = rec
		e.seg = audio.NewSegmenter(audio.SegmentConfig{
			SampleRate: rate,
			Threshold:  cfg.ASR.Threshold,
			SilenceMS:  cfg.ASR.SilenceMS,
			MaxSec:     cfg.ASR.ChunkSec,
			MinSec:     1,
		})
		e.mu.Unlock()
		e.wgWork.Add(1)
		go e.recordLoop(rec)
	}

	e.wgASR.Add(1)
	go e.asrLoop()
	e.wgAna.Add(1)
	go e.analysisLoop()

	e.wgWork.Add(1)
	go e.watchLoop()

	e.emit("session", copySession(sess))
	logx.Infof("lesson", "开始记录 %s (%s)", sess.Title, sess.ID)
	return nil
}

// Stop 结束记录。可重复调用，只有第一次生效。
func (e *Engine) Stop() error {
	e.mu.Lock()
	if !e.running {
		stopping := e.stopping
		e.mu.Unlock()
		if stopping {
			return fmt.Errorf("正在结束上一堂课，请稍候")
		}
		return fmt.Errorf("当前没有进行中的课堂")
	}
	e.running = false
	e.stopping = true
	e.stage = "reporting"
	rec := e.rec
	watchStop := e.watchStop
	sess := e.sess
	e.mu.Unlock()

	if watchStop != nil {
		close(watchStop)
	}
	// 1) 停止录音，等待采集与检测协程退出。
	// recordLoop 会在退出前自己 flush 残留音频（见该函数的 defer），
	// 这里只需要等它把最后一批 job 送进 asrQ。
	if rec != nil {
		rec.Stop()
	}
	e.wgWork.Wait()

	// 2) 等待残余音频转写回填。
	e.waitPending(12 * time.Second)

	e.mu.Lock()
	e.rec = nil
	e.seg = nil
	asrQ := e.asrQ
	anaQ := e.anaQ
	e.mu.Unlock()

	// 3) 关闭转写队列并等待其退出。
	if asrQ != nil {
		close(asrQ)
	}
	e.wgASR.Wait()

	// 4) 收尾最后一页并等待分析完成。
	now := time.Now()
	e.mu.Lock()
	if e.cur != nil {
		e.cur.Duration = int(now.Sub(e.cur.CreatedAt).Seconds())
		e.cur.Status = "pending"
	}
	lastID := ""
	if e.cur != nil {
		lastID = e.cur.ID
	}
	analyze := e.cfg.Lesson.AnalyzeSlides
	sess.EndedAt = &now
	sess.Status = "ended"
	sess.Stats.DurationSec = int(now.Sub(sess.StartedAt).Seconds())
	e.mu.Unlock()

	if lastID != "" && analyze {
		if anaQ != nil {
			select {
			case anaQ <- lastID:
			case <-time.After(3 * time.Second):
			}
		}
	} else if lastID != "" {
		e.mu.Lock()
		if s := e.findSegmentLocked(lastID); s != nil {
			s.Status = "skipped"
		}
		e.mu.Unlock()
	}
	if anaQ != nil {
		close(anaQ)
	}
	e.wgAna.Wait()

	e.mu.Lock()
	e.stage = "idle"
	e.stopping = false
	ended := copySession(e.sess)
	e.mu.Unlock()
	e.persist()
	e.emit("session_ended", ended)
	logx.Infof("lesson", "结束记录 %s", sess.ID)
	return nil
}

// StopAndReport 结束并生成报告。
func (e *Engine) StopAndReport() error {
	if err := e.Stop(); err != nil {
		return err
	}
	return e.GenerateReport(e.SessionCopy())
}

func (e *Engine) recordLoop(rec *audio.Recorder) {
	defer e.wgWork.Done()
	watchStop := e.watchStop
	// 退出前把分段器里还没吐出的音频结算掉。
	// 1.2.5 的写法是「等本协程退出后再 flush」——那时 segCh 已经没有消费者，
	// 请求必然超时，最后一句话（最长 10 秒）就这样丢了。
	defer func() {
		e.mu.Lock()
		seg := e.seg
		e.mu.Unlock()
		if seg == nil {
			return
		}
		if out, ok := seg.Flush(); ok {
			e.enqueueASR([]audio.Segment{out})
		}
	}()
	for {
		select {
		case <-watchStop:
			return
		case req := <-e.segCh:
			e.mu.Lock()
			seg := e.seg
			e.mu.Unlock()
			var out []audio.Segment
			if seg != nil {
				out = req.fn(seg)
			}
			select {
			case req.reply <- out:
			default:
			}
		case pcm, ok := <-rec.Output():
			if !ok {
				return
			}
			e.mu.Lock()
			seg := e.seg
			e.level = rec.Peak()
			e.mu.Unlock()
			if seg == nil {
				continue
			}
			segs := seg.Push(pcm16(pcm))
			e.mu.Lock()
			e.speaking = seg.Speaking()
			e.mu.Unlock()
			if len(segs) > 0 {
				e.enqueueASR(segs)
			}
		}
	}
}

func (e *Engine) enqueueASR(segs []audio.Segment) {
	e.mu.Lock()
	cur := e.cur
	rate := e.cfg.ASR.SampleRate
	if rate <= 0 {
		rate = 16000
	}
	var segID string
	if cur != nil {
		segID = cur.ID
		e.pending[segID]++
	}
	q := e.asrQ
	e.mu.Unlock()
	if segID == "" || q == nil {
		return
	}
	for _, s := range segs {
		wav := audio.EncodeWAV(s.Bytes(), rate)
		select {
		case q <- asrJob{segID: segID, wav: wav, at: s.StartedAt}:
		case <-time.After(3 * time.Second):
			e.mu.Lock()
			e.pending[segID]--
			e.mu.Unlock()
		}
	}
}

func (e *Engine) asrLoop() {
	defer e.wgASR.Done()
	for job := range e.asrQ {
		text, err := e.transcribe(job.wav)
		e.mu.Lock()
		e.pending[job.segID]--
		seg := e.findSegmentLocked(job.segID)
		e.mu.Unlock()
		if err != nil {
			logx.Warnf("asr", "转写失败: %v", err)
			continue
		}
		text = strings.TrimSpace(text)
		// whisper 在静音/噪声上会输出模板句幻觉（"课堂讲课内容，语文、化学…"），
		// 直接落库会把整堂课变成一屏复读。这里丢掉无效片段。
		text = cleanASRText(text)
		if text == "" || seg == nil {
			continue
		}
		e.mu.Lock()
		seg.Transcript = joinTranscript(seg.Transcript, text)
		full := seg.Transcript
		e.mu.Unlock()
		e.emit("transcript", map[string]any{
			"segmentId": job.segID, "text": text, "full": full,
		})
		e.persist()
	}
}

func (e *Engine) transcribe(wav []byte) (string, error) {
	e.mu.Lock()
	cli := e.asrCli
	rate := e.cfg.ASR.SampleRate
	e.mu.Unlock()
	if cli == nil || !cli.Ready() {
		return "", fmt.Errorf("转写未配置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return cli.Transcribe(ctx, wav, rate)
}

func (e *Engine) waitPending(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		n := 0
		for _, v := range e.pending {
			n += v
		}
		e.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
}

// flushSegToASR 结束当前语音段并送去转写。
func (e *Engine) flushSegToASR() []audio.Segment {
	e.mu.Lock()
	hasSeg := e.seg != nil
	ch := e.segCh
	e.mu.Unlock()
	if !hasSeg || ch == nil {
		return nil
	}
	req := segReq{reply: make(chan []audio.Segment, 1)}
	req.fn = func(s *audio.Segmenter) []audio.Segment {
		if out, ok := s.Flush(); ok {
			return []audio.Segment{out}
		}
		return nil
	}
	select {
	case ch <- req:
	case <-time.After(time.Second):
		return nil
	}
	select {
	case out := <-req.reply:
		if len(out) > 0 {
			e.enqueueASR(out)
		}
		return out
	case <-time.After(2 * time.Second):
		return nil
	}
}

func (e *Engine) watchLoop() {
	defer e.wgWork.Done()
	watchStop := e.watchStop
	for {
		e.mu.Lock()
		interval := time.Duration(e.cfg.Capture.IntervalMS) * time.Millisecond
		running := e.running
		e.mu.Unlock()
		if !running {
			return
		}
		if interval < 200*time.Millisecond {
			interval = 700 * time.Millisecond
		}
		select {
		case <-watchStop:
			return
		case <-time.After(interval):
		}
		if !e.isRunning() {
			return
		}
		snap, changed, presenting := e.det.Probe()
		if changed && snap != nil {
			e.rotate("翻页", snap)
			continue
		}
		if !presenting || snap == nil {
			e.maybeTimeSegment()
		}
	}
}

func (e *Engine) isRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) maybeTimeSegment() {
	e.mu.Lock()
	last := e.lastSegAt
	cur := e.cur
	analyze := e.cfg.Lesson.AnalyzeSlides
	e.mu.Unlock()
	if cur == nil || !analyze || time.Since(last) < 5*time.Minute {
		return
	}
	if len([]rune(cur.Transcript)) < 120 {
		return
	}
	e.rotate("时间分段", nil)
}

// rotate 结束当前分段，并以新快照开启下一段。
func (e *Engine) rotate(reason string, snap *capture.Snapshot) {
	// 同一页被重复检测（窗口抖动、手滑回翻）且当前页还没攒下语音时，
	// 不要再开一个新分段，否则报告里会出现一堆重复的空页。
	if reason == "翻页" && snap != nil {
		e.mu.Lock()
		cur := e.cur
		e.mu.Unlock()
		if cur != nil && snap.Index > 0 && cur.SlideIndex == snap.Index &&
			strings.TrimSpace(cur.Transcript) == "" && cur.Status != "done" {
			logx.Debugf("lesson", "忽略同页重复分段（第 %d 页）", snap.Index)
			return
		}
	}

	e.flushSegToASR()

	// 先读取 PPT 文字（涉及文件 IO），必须在加锁前完成，避免自锁。
	boardText := ""
	if snap != nil && snap.FilePath != "" {
		boardText = e.slideText(snap.FilePath, snap.Index)
	}

	// 开课首段如果还没采到任何材料，就把这次快照并入它，不要再开新页。
	// 2026-11-02 地理课、化学课的第 1 页在报告里整页空白，原因就是：
	// 点开始后 1 秒检测到翻页，首段既没截图也没文字也没说话，
	// analyze() 把它当空段 skip 掉，报告第一页就只剩一个时间戳。
	if e.adoptEmptyHead(snap, boardText) {
		return
	}

	now := time.Now()
	e.mu.Lock()
	old := e.cur
	sess := e.sess
	if old != nil {
		old.Duration = int(now.Sub(old.CreatedAt).Seconds())
		if old.Status == "" || old.Status == "recording" {
			old.Status = "pending"
		}
	}
	idx := 1
	if sess != nil {
		idx = len(sess.Segments) + 1
	}
	seg := e.newSegment(idx, snap)
	if snap != nil && snap.Index > 0 {
		seg.SlideIndex = snap.Index
	}
	if boardText != "" {
		seg.BoardText = boardText
	}
	e.cur = seg
	if sess != nil {
		sess.Segments = append(sess.Segments, seg)
	}
	e.lastSegAt = now
	prevID := ""
	if old != nil {
		prevID = old.ID
	}
	analyze := e.cfg.Lesson.AnalyzeSlides
	anaQ := e.anaQ
	e.mu.Unlock()

	rel := e.saveSegmentImage(seg, snap)

	payload := map[string]any{
		"segmentId": seg.ID, "index": seg.Index, "slideIndex": seg.SlideIndex,
		"width": seg.Width, "height": seg.Height, "reason": reason,
	}
	if snap != nil {
		payload["image"] = rel
		payload["app"] = snap.App
		payload["title"] = snap.Title
		payload["source"] = snap.Source
	}
	e.emit("slide", payload)

	if prevID != "" && analyze && anaQ != nil {
		select {
		case anaQ <- prevID:
		default:
		}
	}
	e.persist()
	logx.Infof("lesson", "新分段 #%d（%s）", seg.Index, reason)
}

// adoptEmptyHead 把第一次翻页的快照并入开课首段。
//
// 只在「首段还是空壳」时生效：没有截图、没有画面文字、没有说话、还没绑定页码。
// 这时它对应的其实就是当前这张幻灯片，补上材料比另开一页更符合事实。
// 返回 true 表示已并入，调用方不要再新建分段。
func (e *Engine) adoptEmptyHead(snap *capture.Snapshot, boardText string) bool {
	if snap == nil || snap.Index <= 0 {
		return false
	}
	e.mu.Lock()
	cur := e.cur
	if cur == nil {
		e.mu.Unlock()
		return false
	}
	segs := 0
	if e.sess != nil {
		segs = len(e.sess.Segments)
	}
	empty := cur.ImageFile == "" && strings.TrimSpace(cur.BoardText) == "" &&
		strings.TrimSpace(cur.Transcript) == "" && cur.SlideIndex <= 0
	if !empty || segs > 1 {
		e.mu.Unlock()
		return false
	}
	cur.SlideIndex = snap.Index
	if boardText != "" {
		cur.BoardText = boardText
	}
	// 首段的计时从真正抓到画面的这一刻算起，否则时长会算成整个等待时间。
	cur.CreatedAt = time.Now()
	idx := cur.Index
	e.lastSegAt = time.Now()
	e.mu.Unlock()

	rel := e.saveSegmentImage(cur, snap)
	e.emit("slide", map[string]any{
		"segmentId": cur.ID, "index": idx, "slideIndex": snap.Index,
		"width": cur.Width, "height": cur.Height, "reason": "首段补采",
		"image": rel, "app": snap.App, "title": snap.Title, "source": snap.Source,
	})
	e.persist()
	logx.Infof("lesson", "第 %d 页并入开课首段（避免首页空白）", snap.Index)
	return true
}

// slideText 读取 PPTX 中对应页的文字，用于在没有视觉模型时提供准确的画面内容。
func (e *Engine) slideText(path string, index int) string {
	e.mu.Lock()
	deck := e.deck
	samePath := e.deckPath == path
	e.mu.Unlock()
	if !samePath || deck == nil {
		d, err := capture.ExtractDeck(path)
		if err != nil {
			logx.Debugf("lesson", "读取 PPT 文本失败: %v", err)
			e.mu.Lock()
			e.deckPath = path
			e.deck = nil
			e.mu.Unlock()
			return ""
		}
		e.mu.Lock()
		e.deck = d
		e.deckPath = path
		e.mu.Unlock()
		deck = d
		logx.Infof("lesson", "已从 PPTX 提取 %d 页文字", len(d.Slides))
	}
	return deck.SlideText(index)
}

func (e *Engine) newSegment(idx int, snap *capture.Snapshot) *store.Segment {
	seg := &store.Segment{
		ID:        fmt.Sprintf("s%d-%d", idx, time.Now().UnixNano()%1e7),
		Index:     idx,
		CreatedAt: time.Now(),
		Status:    "recording",
	}
	if snap != nil {
		seg.Width = snap.Width
		seg.Height = snap.Height
		seg.SlideIndex = snap.Index
	}
	return seg
}

func (e *Engine) saveSegmentImage(seg *store.Segment, snap *capture.Snapshot) string {
	if snap == nil || snap.Image == nil {
		return ""
	}
	e.mu.Lock()
	q := e.cfg.Capture.JPEGQuality
	sessID := ""
	if e.sess != nil {
		sessID = e.sess.ID
	}
	e.mu.Unlock()
	if q <= 0 {
		q = 88
	}
	name := fmt.Sprintf("slide-%03d.jpg", seg.Index)
	path := filepath.Join(e.captures, name)
	if err := capture.SaveImage(snap.Image, path, q); err != nil {
		logx.Warnf("lesson", "保存截图失败: %v", err)
		return ""
	}
	e.mu.Lock()
	seg.ImageFile = name
	seg.SHA = fmt.Sprintf("%016x", snap.Hash)
	e.mu.Unlock()
	return filepath.ToSlash(filepath.Join(sessID, name))
}

func (e *Engine) analysisLoop() {
	defer e.wgAna.Done()
	for id := range e.anaQ {
		e.analyze(id)
	}
}

func (e *Engine) analyze(segID string) {
	e.mu.Lock()
	seg := e.findSegmentLocked(segID)
	client := e.client
	subject := ""
	prevSummary := ""
	vision := e.cfg.Provider.Vision
	captures := e.captures
	if e.sess != nil {
		subject = e.sess.Subject
		for i, s := range e.sess.Segments {
			if s.ID == segID && i > 0 {
				prevSummary = e.sess.Segments[i-1].Summary
			}
		}
	}
	e.mu.Unlock()
	if seg == nil {
		return
	}
	// 开课时创建的第一个空分段没有画面也没有语音，不必消耗模型。
	// 但要留下痕迹：1.2.5 这里直接 return，报告里那页只剩时间戳，
	// 而日志里一句话都没有，根本查不出为什么空。
	if seg.ImageFile == "" && strings.TrimSpace(seg.BoardText) == "" && len([]rune(seg.Transcript)) < 40 {
		logx.Infof("lesson", "第 %d 页没有画面与语音，跳过分析", seg.Index)
		e.mu.Lock()
		seg.Status = "skipped"
		seg.Error = "本页没有采集到画面与语音"
		e.mu.Unlock()
		return
	}
	if client == nil {
		e.mu.Lock()
		seg.Status = "skipped"
		e.mu.Unlock()
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		n := e.pending[segID]
		e.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(120 * time.Millisecond)
	}

	e.mu.Lock()
	seg.Status = "analyzing"
	e.stage = "analyzing"
	transcript := seg.Transcript
	slideIndex := seg.SlideIndex
	imageFile := seg.ImageFile
	duration := seg.Duration
	boardText := seg.BoardText
	e.mu.Unlock()

	e.emit("segment_status", map[string]any{"segmentId": segID, "status": "analyzing"})

	e.mu.Lock()
	skill := e.skillPrompt
	e.mu.Unlock()
	prompt := analysisUser(subject, slideIndex, duration, boardText, transcript, prevSummary) + skill
	msgs := []llm.Message{llm.TextMessage(llm.RoleSystem, analysisSystem)}
	usedImage := false
	if imageFile != "" && vision {
		if raw, err := os.ReadFile(filepath.Join(captures, imageFile)); err == nil {
			msgs = append(msgs, llm.UserVision(prompt, base64.StdEncoding.EncodeToString(raw), "image/jpeg"))
			usedImage = true
		}
	}
	if !usedImage {
		hint := "（本页没有可用图片，请依据画面文字与转写文本分析）"
		if strings.TrimSpace(boardText) == "" {
			hint = "（本页既没有图片也没有画面文字，只有转写文本；如果转写也为空，请输出空笔记并把 copying_score 设为 0）"
		}
		msgs = append(msgs, llm.UserText(prompt+"\n\n"+hint))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	e.mu.Lock()
	followUp := e.lastSubject == subject && subject != ""
	e.lastSubject = subject
	e.mu.Unlock()
	res, err := client.Chat(ctx, msgs, llm.Options{Subject: subject, FollowUp: followUp, Temperature: 0.3, MaxTokens: 2048, JSON: true})
	if err != nil {
		logx.Warnf("lesson", "分析失败: %v", err)
		e.mu.Lock()
		seg.Status = "failed"
		seg.Error = err.Error()
		e.aiErr = err.Error()
		e.mu.Unlock()
		e.emit("segment_status", map[string]any{"segmentId": segID, "status": "failed", "error": err.Error()})
		return
	}
	out, derr := decodeAnalysis(res.Text)
	parseErr := ""
	if derr != nil {
		// 解析失败时绝不把原始 JSON 当笔记——那会在报告里显示成一堆大括号。
		logx.Warnf("lesson", "模型输出解析失败: %v；原文开头：%s", derr, clipLog(res.Text, 160))
		switch {
		case looksLikeRawJSON(res.Text) || looksLikeJSONFragment(res.Text):
			// 结构不对、字段也救不出来：宁可留空，也不要把引号和大括号
			// 糊进报告。同时把原因写进 segment，前端能看到是模型没按格式回。
			out.Notes = ""
			out.Summary = ""
			parseErr = "模型回复的结构无法识别，本页内容已丢弃"
		default:
			out.Notes = res.Text
			out.Summary = firstLine(res.Text)
		}
		if out.Summary == "" && out.Notes != "" {
			out.Summary = firstLine(out.Notes)
		}
	}
	// 清理数学符号乱码（网页版模型常把公式拆成逐字 Unicode 变体）。
	cleaned := normalizeAll(out.BoardText, out.Summary, out.Notes)
	out.BoardText, out.Summary, out.Notes = cleaned[0], cleaned[1], cleaned[2]
	out.CopyingEvidence = normalizeModelText(out.CopyingEvidence)
	for i := range out.KeyPoints {
		out.KeyPoints[i] = normalizeModelText(out.KeyPoints[i])
	}
	for i := range out.Tips {
		out.Tips[i] = normalizeModelText(out.Tips[i])
	}
	// 兜底：任何字段都不允许以 JSON 形态残留。
	if looksLikeRawJSON(out.Notes) || looksLikeJSONFragment(out.Notes) {
		out.Notes = ""
	}
	if looksLikeRawJSON(out.Summary) || looksLikeJSONFragment(out.Summary) || strings.TrimSpace(out.Summary) == "{" {
		out.Summary = ""
	}
	e.mu.Lock()
	seg.BoardText = out.BoardText
	seg.Summary = out.Summary
	seg.Notes = out.Notes
	seg.KeyPoints = out.KeyPoints
	seg.Tips = out.Tips
	seg.CopyScore = clamp(out.CopyingScore)
	seg.CopyEvidence = strings.TrimSpace(out.CopyingEvidence)
	if seg.CopyEvidence == "" {
		if out.IsCopying == nil {
			seg.CopyEvidence = "本页缺少语音转写，无法对照画面文字判断"
		} else {
			seg.CopyEvidence = copyFallbackEvidence(seg.CopyScore)
		}
	}
	seg.Notes = dedupeNotes(seg.Notes)
	if out.IsCopying != nil {
		seg.Copying = *out.IsCopying
	} else {
		// 没有语音证据时不能给出"未照搬"的结论。
		seg.Copying = false
		seg.CopyScore = -1
	}
	seg.Error = parseErr
	seg.Status = "done"
	seg.Model = res.Model
	seg.Tokens = res.Usage.TotalTokens
	if e.sess != nil {
		e.sess.Stats.AICalls++
		e.sess.Stats.Tokens += res.Usage.TotalTokens
	}
	e.stage = "listening"
	e.aiErr = ""
	cp := *seg
	e.mu.Unlock()

	e.persist()
	e.emit("analysis", map[string]any{"segment": cp, "usage": res.Usage})
	logx.Infof("lesson", "第 %d 页分析完成（%d tokens）", cp.Index, res.Usage.TotalTokens)
}

func (e *Engine) findSegmentLocked(id string) *store.Segment {
	if e.sess == nil {
		return nil
	}
	for _, s := range e.sess.Segments {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// CaptureNow 手动截取当前画面并生成新分段。
func (e *Engine) CaptureNow() error {
	if !e.isRunning() {
		return fmt.Errorf("请先开始记录")
	}
	snap, err := e.det.CaptureNow()
	if err != nil {
		return err
	}
	e.rotate("手动截图", snap)
	return nil
}

// Ask 就当前课堂提问。
func (e *Engine) Ask(question string) (string, error) {
	e.mu.Lock()
	client := e.client
	sess := e.sess
	e.mu.Unlock()
	if client == nil {
		return "", fmt.Errorf("模型未就绪")
	}
	if sess == nil {
		return "", fmt.Errorf("还没有课堂记录")
	}
	ctxText := e.buildContext(6000)
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleSystem, chatSystem),
		llm.UserText(chatUser(ctxText, question)),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	res, err := client.Chat(ctx, msgs, llm.Options{Subject: sess.Subject, Temperature: 0.4, MaxTokens: 2048})
	if err != nil {
		return "", err
	}
	now := time.Now()
	e.mu.Lock()
	if e.sess != nil {
		e.sess.Messages = append(e.sess.Messages,
			store.ChatMessage{ID: fmt.Sprintf("u%d", now.UnixNano()), Role: "user", Content: question, CreatedAt: now},
			store.ChatMessage{ID: fmt.Sprintf("a%d", now.UnixNano()+1), Role: "assistant", Content: res.Text, CreatedAt: now},
		)
	}
	e.mu.Unlock()
	e.persist()
	return res.Text, nil
}

func (e *Engine) buildContext(limit int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "学科：%s\n课题：%s\n", e.sess.Subject, e.sess.Title)
	for _, s := range e.sess.Segments {
		if s.Summary == "" && s.Notes == "" && s.Transcript == "" {
			continue
		}
		fmt.Fprintf(&sb, "\n--- 第 %d 页 ---\n", s.Index)
		if s.Summary != "" {
			fmt.Fprintf(&sb, "要点：%s\n", s.Summary)
		}
		if s.Notes != "" {
			fmt.Fprintf(&sb, "笔记：%s\n", truncate(s.Notes, 700))
		}
		if s.Transcript != "" {
			fmt.Fprintf(&sb, "录音：%s\n", truncate(s.Transcript, 800))
		}
		if sb.Len() > limit*2 {
			break
		}
	}
	out := sb.String()
	if len([]rune(out)) > limit {
		out = string([]rune(out)[:limit])
	}
	return out
}

func joinTranscript(old, add string) string {
	old = strings.TrimSpace(old)
	add = strings.TrimSpace(add)
	if old == "" {
		return add
	}
	if add == "" {
		return old
	}
	sep := ""
	if !strings.HasSuffix(old, "。") && !strings.HasSuffix(old, "，") && !strings.HasSuffix(old, "？") &&
		!strings.HasSuffix(old, "！") && !strings.HasSuffix(old, "；") {
		sep = "，"
	}
	return old + sep + add
}

// extractField 在 JSON 解析失败时按字段名兜底取值。
// 支持 "notes": "文本" 与 "notes": ["行1","行2"] 两种写法（网页版模型两种都会出）。
func extractField(raw, key string) string {
	// 数组形式优先：字符串正则会把数组里的第一个元素误当整段。
	reArr := regexp.MustCompile(`"` + key + `"\s*:\s*\[([\s\S]*?)\]`)
	if m := reArr.FindStringSubmatch(raw); m != nil {
		var parts []string
		for _, mm := range regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(m[1], -1) {
			if v := unquoteLoose(mm[1]); strings.TrimSpace(v) != "" {
				parts = append(parts, strings.TrimSpace(v))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	re := regexp.MustCompile(`"` + key + `"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	m := re.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(unquoteLoose(m[1]))
}

func unquoteLoose(s string) string {
	if v, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return v
	}
	return strings.ReplaceAll(s, `\n`, "\n")
}

// dedupeNotes 去掉模型偶发的连续重复行。
func dedupeNotes(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	var prev string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t != "" && t == prev {
			continue
		}
		if t != "" {
			prev = t
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// EditNote 手动修改某一页的笔记或要点。
//
// 1.2.5 的 HTTP 层直接拿 Session() 的裸指针在锁外改字段，而引擎此时可能正在
// analyze() 里持锁写同一个 segment——轻则改动丢失，重则把会话 JSON 写坏。
// 修改统一走这里：持锁改、解锁后落盘。
func (e *Engine) EditNote(sessionID, segID, notes, summary string) error {
	e.mu.Lock()
	if e.sess != nil && (sessionID == "" || e.sess.ID == sessionID) {
		for _, seg := range e.sess.Segments {
			if seg.ID != segID {
				continue
			}
			if notes != "" {
				seg.Notes = notes
			}
			if summary != "" {
				seg.Summary = summary
			}
			cp := *seg
			e.mu.Unlock()
			e.persist()
			e.emit("analysis", map[string]any{"segment": cp, "edited": true})
			return nil
		}
		e.mu.Unlock()
		return fmt.Errorf("找不到该页")
	}
	e.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("当前没有进行中的课堂")
	}
	sess, err := e.store.Load(sessionID)
	if err != nil {
		return err
	}
	found := false
	for _, seg := range sess.Segments {
		if seg.ID == segID {
			if notes != "" {
				seg.Notes = notes
			}
			if summary != "" {
				seg.Summary = summary
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("找不到该页")
	}
	return e.store.Save(sess)
}

// AppendUtterance 手动补录一段转写（没有麦克风或识别失败时使用）。
func (e *Engine) AppendUtterance(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("内容为空")
	}
	e.mu.Lock()
	if !e.running || e.cur == nil {
		e.mu.Unlock()
		return fmt.Errorf("当前没有进行中的课堂")
	}
	e.cur.Transcript = joinTranscript(e.cur.Transcript, text)
	segID := e.cur.ID
	full := e.cur.Transcript
	e.mu.Unlock()
	e.emit("transcript", map[string]any{"segmentId": segID, "text": text, "full": full})
	e.persist()
	return nil
}

func pcm16(b []byte) []int16 {
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(uint16(b[i*2]) | uint16(b[i*2+1])<<8)
	}
	return out
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n。"); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	if len([]rune(s)) > 60 {
		return string([]rune(s)[:60])
	}
	return s
}

// clipLog 截断长文本用于日志，避免把整段模型输出写进日志文件。
func clipLog(s string, n int) string {
	t := strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(t)
	if len(r) <= n {
		return t
	}
	return string(r[:n]) + "…"
}
