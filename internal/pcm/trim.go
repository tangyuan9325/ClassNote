package pcm

import "math"

// 本文件做转写前的静音裁剪。
//
// 分段器为了保证不吞字，会把句尾最多 700ms 的静音一起打包送进来（见
// internal/audio/segmenter.go 的 "语音尾部也保留"）。whisper 拿到这种
// "一句话 + 一长段静音" 的音频时，会把静音当成解码的起点，顺着上文往下编，
// 这是它在课堂上稳定复现幻觉的主要入口之一。
//
// 处理办法很克制：只裁掉首尾明显低于本段自身响度的部分，中间一律不动。
// 门限取「本段峰值的 18%」而不是固定值，麦克风增益偏低时也不会把正常
// 说话裁成静音。

const (
	// silenceFloor 是绝对下限：低于它基本只可能是底噪。
	silenceFloor = 0.0035
	// headKeep / tailKeep 是保留的余量，避免把字头字尾的辅音切掉。
	headKeepMS = 80
	tailKeepMS = 240
	// minKeepMS 是裁剪后仍算有效音频的最短长度。
	minKeepMS = 300
	// minActiveMS 是「真的有人在说话」所需的最短有声时长，挡掉敲击声。
	minActiveMS = 100
)

// frameRMS 按 20ms 一帧计算归一化 RMS。
func frameRMS(pcm []byte, frameSamples int) []float64 {
	n := len(pcm) / 2
	frames := n / frameSamples
	if frames <= 0 {
		return nil
	}
	out := make([]float64, frames)
	for f := 0; f < frames; f++ {
		base := f * frameSamples * 2
		var sum float64
		for i := 0; i < frameSamples; i++ {
			lo := pcm[base+i*2]
			hi := pcm[base+i*2+1]
			v := int16(uint16(lo) | uint16(hi)<<8)
			s := float64(v) / 32768
			sum += s * s
		}
		out[f] = math.Sqrt(sum / float64(frameSamples))
	}
	return out
}

// trimSilence 裁掉首尾静音；整段都是静音时返回 nil。
func TrimSilence(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	frameSamples := sampleRate / 50
	if frameSamples < 80 {
		frameSamples = 80
	}
	rms := frameRMS(pcm, frameSamples)
	if len(rms) == 0 {
		return nil
	}
	peak := 0.0
	for _, v := range rms {
		if v > peak {
			peak = v
		}
	}
	// 整段都在底噪水平：不送转写，直接当空。
	if peak < silenceFloor {
		return nil
	}
	gate := peak * 0.18
	if gate < silenceFloor {
		gate = silenceFloor
	}
	first, last, active := -1, -1, 0
	for i, v := range rms {
		if v >= gate {
			if first < 0 {
				first = i
			}
			last = i
			active++
		}
	}
	if first < 0 {
		return nil
	}
	// 只有一两帧超过门限的是敲击声、翻页声这类瞬态噪声，不是说话。
	// 送进去 whisper 同样会编出一句话来，直接丢掉。
	const frameMS = 1000 / 50
	if active*frameMS < minActiveMS {
		return nil
	}
	headPad := headKeepMS / frameMS
	tailPad := tailKeepMS / frameMS
	first -= headPad
	if first < 0 {
		first = 0
	}
	last += tailPad
	if last >= len(rms) {
		last = len(rms) - 1
	}
	start := first * frameSamples * 2
	end := (last + 1) * frameSamples * 2
	if end > len(pcm) {
		end = len(pcm)
	}
	if end-start < sampleRate*2*minKeepMS/1000 {
		return nil
	}
	return pcm[start:end]
}
