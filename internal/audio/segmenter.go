package audio

import (
	"math"
	"time"
)

// SegmentConfig 是切分参数。
type SegmentConfig struct {
	SampleRate int
	Threshold  float64 // RMS 阈值 0-1
	SilenceMS  int     // 静音多久算一句话结束
	MaxSec     int     // 单段最长
	MinSec     int     // 单段最短
	PreRollMS  int     // 语音前预录
	AutoGain   bool
}

// Segment 是一段语音。
type Segment struct {
	PCM       []int16
	Peak      float64
	StartedAt time.Time
	EndedAt   time.Time
}

// Duration 返回时长。
func (s Segment) Duration() time.Duration {
	if s.EndedAt.IsZero() {
		return time.Duration(len(s.PCM)*1000/16000) * time.Millisecond
	}
	return s.EndedAt.Sub(s.StartedAt)
}

// Bytes 返回 PCM16 字节。
func (s Segment) Bytes() []byte {
	out := make([]byte, len(s.PCM)*2)
	for i, v := range s.PCM {
		out[i*2] = byte(uint16(v))
		out[i*2+1] = byte(uint16(v) >> 8)
	}
	return out
}

// Segmenter 做基于能量的语音活动检测。
type Segmenter struct {
	cfg     SegmentConfig
	frame   int // 每帧样本数
	pre     []int16
	preMax  int
	cur     []int16
	speech  bool
	silent  int // 已连续静音帧数
	noise   float64
	level   float64
	started time.Time
	adapted bool

	// 句间短停顿（< SilenceMS）的位置，用于到达长度上限时回溯切分：
	// 把最后一句话留给下一批，而不是从中间切断。
	pauseAt int
	lowRun  int
	lowFrom int
}

// NewSegmenter 创建切分器。
func NewSegmenter(cfg SegmentConfig) *Segmenter {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 16000
	}
	if cfg.SilenceMS <= 0 {
		cfg.SilenceMS = 700
	}
	if cfg.MaxSec <= 0 {
		cfg.MaxSec = 12
	}
	if cfg.MinSec <= 0 {
		cfg.MinSec = 1
	}
	if cfg.PreRollMS <= 0 {
		cfg.PreRollMS = 320
	}
	frame := cfg.SampleRate / 50 // 20ms
	if frame < 80 {
		frame = 80
	}
	return &Segmenter{
		cfg:    cfg,
		frame:  frame,
		preMax: cfg.SampleRate * cfg.PreRollMS / 1000,
		noise:  0.004,
	}
}

// Level 返回最近一帧的响度。
func (s *Segmenter) Level() float64 { return s.level }

// Speaking 表示当前是否处于说话状态。
func (s *Segmenter) Speaking() bool { return s.speech }

// Push 输入 PCM16 数据，返回已完成的语音段。
func (s *Segmenter) Push(pcm []int16) []Segment {
	var out []Segment
	for len(pcm) >= s.frame {
		chunk := pcm[:s.frame]
		pcm = pcm[s.frame:]
		rms := rmsOf(chunk)
		s.level = rms
		th := s.threshold()
		active := rms > th
		if !active {
			// 环境噪声慢速自适应。
			s.noise = s.noise*0.97 + rms*0.03
		}
		if active {
			// 短停顿结束、重新出声：把刚才停顿的起点记为候选句界。
			if s.lowRun*20 >= 80 && s.lowFrom > 0 && s.lowFrom < len(s.cur) {
				s.pauseAt = s.lowFrom
			}
			s.lowRun = 0
			if !s.speech {
				s.speech = true
				s.started = time.Now()
				s.cur = append(s.cur[:0], s.pre...)
				s.pauseAt = 0
			}
			s.silent = 0
			s.cur = append(s.cur, chunk...)
		} else {
			if s.speech {
				if s.lowRun == 0 {
					s.lowFrom = len(s.cur)
				}
				s.lowRun++
				s.silent++
				// 语音尾部也保留，避免吞字。
				s.cur = append(s.cur, chunk...)
				if s.silent*20 >= s.cfg.SilenceMS {
					if seg, ok := s.finish(); ok {
						out = append(out, seg)
					}
				}
			} else {
				s.pushPre(chunk)
				s.lowRun = 0
			}
		}
		if s.speech && len(s.cur) >= s.cfg.SampleRate*s.cfg.MaxSec {
			if seg, ok := s.finishAtLimit(); ok {
				out = append(out, seg)
			}
		}
	}
	return out
}

// Flush 强制结束当前语音段。
func (s *Segmenter) Flush() (Segment, bool) {
	if !s.speech {
		return Segment{}, false
	}
	return s.finish()
}

// finishAtLimit 到达长度上限时的切分：优先回溯到最近一次短停顿，
// 把最后一句留在 cur 里归入下一批，只把前面的内容送去转写。
// 没有可用的句界（例如持续连读）时退回整段硬切，避免无限增长。
func (s *Segmenter) finishAtLimit() (Segment, bool) {
	rate := s.cfg.SampleRate
	tailMin := rate / 5 // 至少留 200ms 给下一批
	headMin := rate * s.cfg.MinSec
	if s.pauseAt >= headMin && len(s.cur)-s.pauseAt >= tailMin {
		head := append([]int16(nil), s.cur[:s.pauseAt]...)
		tail := append([]int16(nil), s.cur[s.pauseAt:]...)
		seg := s.makeSegment(head, s.started, time.Now())
		s.cur = tail
		s.started = time.Now().Add(-time.Duration(len(tail)) * time.Second / time.Duration(rate))
		s.pauseAt = 0
		s.silent = 0
		s.lowRun = 0
		s.lowFrom = 0
		return seg, true
	}
	return s.finish()
}

func (s *Segmenter) finish() (Segment, bool) {
	cur := s.cur
	s.cur = nil
	s.speech = false
	s.silent = 0
	s.pauseAt = 0
	s.lowRun = 0
	s.lowFrom = 0
	minLen := s.cfg.SampleRate * s.cfg.MinSec
	if len(cur) < minLen {
		return Segment{}, false
	}
	return s.makeSegment(cur, s.started, time.Now()), true
}

func (s *Segmenter) makeSegment(pcm []int16, started, ended time.Time) Segment {
	peak := 0.0
	for _, v := range pcm {
		f := math.Abs(float64(v)) / 32768
		if f > peak {
			peak = f
		}
	}
	return Segment{PCM: pcm, Peak: peak, StartedAt: started, EndedAt: ended}
}

func (s *Segmenter) pushPre(chunk []int16) {
	s.pre = append(s.pre, chunk...)
	if len(s.pre) > s.preMax {
		s.pre = s.pre[len(s.pre)-s.preMax:]
	}
}

func (s *Segmenter) threshold() float64 {
	th := s.cfg.Threshold
	if th <= 0 {
		th = 0.01
	}
	// 自适应阈值：以噪声底为基准，但不低于配置值的一半。
	adapt := s.noise * 3.4
	if adapt < th*0.6 {
		adapt = th * 0.6
	}
	if adapt > 0.12 {
		adapt = 0.12
	}
	return adapt
}

func rmsOf(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, v := range pcm {
		f := float64(v) / 32768
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(pcm)))
}
