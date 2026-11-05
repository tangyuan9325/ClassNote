package pcm

import (
	"math"
	"testing"
)

// pcm 生成 n 毫秒的音频：amp>0 时是正弦波，amp=0 时是静音。
func pcm(ms int, amp float64, rate int) []byte {
	n := rate * ms / 1000
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := 0.0
		if amp > 0 {
			v = amp * math.Sin(2*math.Pi*220*float64(i)/float64(rate))
		}
		s := int16(v * 32767)
		out[i*2] = byte(uint16(s))
		out[i*2+1] = byte(uint16(s) >> 8)
	}
	return out
}

func TestTrimSilenceDropsPureSilence(t *testing.T) {
	if got := TrimSilence(pcm(1200, 0, 16000), 16000); len(got) != 0 {
		t.Fatalf("纯静音应该整段丢弃，实际留下 %d 字节", len(got))
	}
}

func TestTrimSilenceKeepsSpeechAndCutsTail(t *testing.T) {
	// 800ms 说话 + 700ms 尾巴静音：这正是分段器送出来的形状
	in := append(pcm(800, .3, 16000), pcm(700, 0, 16000)...)
	out := TrimSilence(in, 16000)
	if len(out) == 0 {
		t.Fatal("有语音却被整段裁掉了")
	}
	if len(out) >= len(in) {
		t.Fatalf("尾部静音没被裁掉：%d -> %d", len(in), len(out))
	}
	// 说话部分（16000 样本 = 32000 字节）必须基本保留
	if len(out) < 32000*9/10 {
		t.Fatalf("裁得太多，语音只剩 %d 字节", len(out))
	}
}

func TestTrimSilenceKeepsQuietSpeech(t *testing.T) {
	// 增益偏低（RMS 约 0.007，接近分段器门限）：不能当成静音丢掉
	in := append(pcm(200, 0, 16000), pcm(900, .01, 16000)...)
	in = append(in, pcm(200, 0, 16000)...)
	out := TrimSilence(in, 16000)
	if len(out) == 0 {
		t.Fatal("小声说话被误判成静音")
	}
	if len(out) < 16000*2*7/10 {
		t.Fatalf("小声说话被裁掉太多：%d 字节", len(out))
	}
}

func TestTrimSilenceIgnoresShortClicks(t *testing.T) {
	// 一次 20ms 的敲击声夹在静音里，不该被当成一句话送转写
	in := append(pcm(600, 0, 16000), pcm(20, .5, 16000)...)
	in = append(in, pcm(600, 0, 16000)...)
	if got := TrimSilence(in, 16000); len(got) != 0 {
		t.Fatalf("孤立敲击声应该丢弃，实际 %d 字节", len(got))
	}
}
