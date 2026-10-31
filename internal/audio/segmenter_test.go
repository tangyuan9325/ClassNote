package audio

import (
	"math"
	"testing"
)

func toneFrame(n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(math.Sin(2*math.Pi*440*float64(i)/16000) * amp * 32767)
	}
	return out
}

func silenceFrame(n int) []int16 { return make([]int16, n) }

// 到达长度上限时应回溯到最后一次短停顿，把最后一句留给下一批。
func TestSegmenterLimitCarriesLastSentence(t *testing.T) {
	s := NewSegmenter(SegmentConfig{
		SampleRate: 16000, Threshold: 0.01, SilenceMS: 700, MaxSec: 2, MinSec: 1,
	})
	// 2 秒最大长度：1.3 秒语音 + 0.1 秒停顿 + 1 秒语音
	frame := 320
	var got []Segment
	push := func(pcm []int16) {
		got = append(got, s.Push(pcm)...)
	}
	for i := 0; i < 65; i++ { // 1.3s
		push(toneFrame(frame, 0.2))
	}
	for i := 0; i < 5; i++ { // 100ms 短停顿（< SilenceMS）
		push(silenceFrame(frame))
	}
	for i := 0; i < 50; i++ { // 1.0s
		push(toneFrame(frame, 0.2))
	}
	if len(got) != 1 {
		t.Fatalf("到达上限后应产出 1 批，实际 %d", len(got))
	}
	first := len(got[0].PCM) / 16 // ms
	if first < 1200 || first > 1400 {
		t.Fatalf("第一批应停在短停顿处约 1300ms，实际 %dms", first)
	}
	tail, ok := s.Flush()
	if !ok {
		t.Fatal("剩余的最后一句应能 Flush 出来")
	}
	ms := len(tail.PCM) / 16
	if ms < 900 || ms > 1150 {
		t.Fatalf("最后一句应保留约 1000ms，实际 %dms", ms)
	}
}

// 没有停顿的持续连读退回硬切，不能无限增长。
func TestSegmenterLimitWithoutPause(t *testing.T) {
	s := NewSegmenter(SegmentConfig{
		SampleRate: 16000, Threshold: 0.01, SilenceMS: 700, MaxSec: 1, MinSec: 1,
	})
	frame := 320
	var got []Segment
	for i := 0; i < 75; i++ { // 1.5s 连续语音
		got = append(got, s.Push(toneFrame(frame, 0.2))...)
	}
	if len(got) != 1 {
		t.Fatalf("硬切应产出 1 批，实际 %d", len(got))
	}
	ms := len(got[0].PCM) / 16
	if ms < 950 || ms > 1050 {
		t.Fatalf("硬切应约 1000ms，实际 %dms", ms)
	}
}
