// Package audio 通过 Windows waveIn 接口采集麦克风音频并做静音切分。
package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"classnote/internal/logx"
)

var (
	winmm = windows.NewLazySystemDLL("winmm.dll")

	procWaveInGetNumDevs      = winmm.NewProc("waveInGetNumDevs")
	procWaveInOpen            = winmm.NewProc("waveInOpen")
	procWaveInClose           = winmm.NewProc("waveInClose")
	procWaveInPrepareHeader   = winmm.NewProc("waveInPrepareHeader")
	procWaveInUnprepareHeader = winmm.NewProc("waveInUnprepareHeader")
	procWaveInAddBuffer       = winmm.NewProc("waveInAddBuffer")
	procWaveInStart           = winmm.NewProc("waveInStart")
	procWaveInStop            = winmm.NewProc("waveInStop")
	procWaveInReset           = winmm.NewProc("waveInReset")
	procWaveInErrorText       = winmm.NewProc("waveInGetErrorTextW")
)

const (
	// WAVE_MAPPER 是 (UINT_PTR)-1；注意不是 ^uint32(0)，
	// 在 64 位上那是设备号 4294967295，waveInOpen 会失败。
	waveMapper    = ^uintptr(0)
	whdrDone      = 0x00000001
	whdrPrepared  = 0x00000002
	callbackNull  = 0x00000000
	waveFormatPCM = 1
)

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

type waveHdr struct {
	Data          uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

// Device 描述录音设备。
type Device struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// Devices 返回可用输入设备数量（不展开名称，需要 COM 才能取到，此处仅报数量）。
func Devices() int {
	n, _, _ := procWaveInGetNumDevs.Call()
	return int(n)
}

// Recorder 是麦克风采集器。
type Recorder struct {
	mu      sync.Mutex
	handle  uintptr
	headers []*waveHdr
	bufs    [][]byte
	running bool
	out     chan []byte
	stopCh  chan struct{}
	wg      sync.WaitGroup
	rate    int
	peak    float64
	err     error
	// dropped 是被消费端堵掉而丢弃的音频块数。
	// 1.2.5 丢得很安静：这里没有计数，Err() 也永远是 nil，
	// 麦克风中途被拔掉或磁盘卡住导致的大量丢音，事后完全查不出来。
	dropped int
}

// New 创建采集器。
func New(sampleRate int) *Recorder {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	return &Recorder{rate: sampleRate, out: make(chan []byte, 32)}
}

// Output 返回 PCM 数据流（16bit 单声道小端）。
func (r *Recorder) Output() <-chan []byte { return r.out }

// Peak 返回最近一次峰值（0-1）。
func (r *Recorder) Peak() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// Err 返回采集过程中的错误。
// 持续丢音也报出来：单块丢弃无所谓，成片丢弃意味着转写会缺内容。
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	if r.dropped >= 20 {
		return fmt.Errorf("音频采集丢块 %d 次，转写可能不完整", r.dropped)
	}
	return nil
}

// Dropped 返回被丢弃的音频块数。
func (r *Recorder) Dropped() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Start 开始采集。
func (r *Recorder) Start(device int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return nil
	}
	if n := Devices(); n <= 0 {
		return errors.New("未检测到录音设备")
	}
	fmtTag := uint16(waveFormatPCM)
	wf := waveFormatEx{
		FormatTag:      fmtTag,
		Channels:       1,
		SamplesPerSec:  uint32(r.rate),
		AvgBytesPerSec: uint32(r.rate * 2),
		BlockAlign:     2,
		BitsPerSample:  16,
	}
	devID := uintptr(device)
	if device < 0 {
		devID = waveMapper
	}
	var h uintptr
	open := func(id uintptr) uintptr {
		var hh uintptr
		ret, _, _ := procWaveInOpen.Call(uintptr(unsafe.Pointer(&hh)), id,
			uintptr(unsafe.Pointer(&wf)), 0, 0, callbackNull)
		if ret == 0 {
			h = hh
		}
		return ret
	}
	ret := open(devID)
	logx.Debugf("audio", "waveInOpen dev=%d ret=%d", devID, ret)
	// 偶发 MMSYSERR_ERROR：通常是上一个采集器刚释放、设备还没就绪，稍等重试。
	for attempt := 0; ret != 0 && attempt < 3; attempt++ {
		time.Sleep(250 * time.Millisecond)
		ret = open(devID)
		logx.Debugf("audio", "waveInOpen retry dev=%d ret=%d", devID, ret)
	}
	// 默认设备（WAVE_MAPPER）打不开时，逐个尝试枚举到的录音设备。
	// 常见于只插了 USB 麦克风、系统默认设备被禁用，或默认设备被独占的情况。
	if ret != 0 && device < 0 {
		for i := 0; i < Devices(); i++ {
			r2 := open(uintptr(i))
			logx.Debugf("audio", "waveInOpen fallback dev=%d ret=%d", i, r2)
			if r2 == 0 {
				ret = 0
				break
			}
			ret = r2
		}
	}
	if ret != 0 {
		return fmt.Errorf("打开录音设备失败 (code=%d)，共 %d 个设备：%s",
			int32(ret), Devices(), waveErrText(ret))
	}
	r.handle = h
	// 4 个 200ms 缓冲，轮转复用。
	bufSize := r.rate / 5 * 2
	r.headers = make([]*waveHdr, 4)
	r.bufs = make([][]byte, 4)
	for i := range r.headers {
		buf := make([]byte, bufSize)
		r.bufs[i] = buf
		hdr := &waveHdr{Data: uintptr(unsafe.Pointer(&buf[0])), BufferLength: uint32(bufSize)}
		r.headers[i] = hdr
		if ret, _, _ := procWaveInPrepareHeader.Call(h, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr)); ret != 0 {
			r.closeLocked()
			return fmt.Errorf("准备录音缓冲失败 (code=%d)", ret)
		}
		if ret, _, _ := procWaveInAddBuffer.Call(h, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr)); ret != 0 {
			r.closeLocked()
			return fmt.Errorf("提交录音缓冲失败 (code=%d)", ret)
		}
	}
	if ret, _, _ := procWaveInStart.Call(h); ret != 0 {
		r.closeLocked()
		return fmt.Errorf("启动录音失败 (code=%d)", ret)
	}
	r.running = true
	r.stopCh = make(chan struct{})
	r.wg.Add(1)
	go r.poll()
	return nil
}

func (r *Recorder) poll() {
	defer r.wg.Done()
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.mu.Lock()
			if !r.running {
				r.mu.Unlock()
				return
			}
			handle := r.handle
			hdrs := r.headers
			bufs := r.bufs
			r.mu.Unlock()
			for i, hdr := range hdrs {
				if hdr.Flags&whdrDone == 0 {
					continue
				}
				n := int(hdr.BytesRecorded)
				var chunk []byte
				if n > 0 && n <= len(bufs[i]) {
					chunk = make([]byte, n)
					copy(chunk, bufs[i][:n])
				}
				hdr.Flags &^= whdrDone
				hdr.BytesRecorded = 0
				_, _, _ = procWaveInAddBuffer.Call(handle, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
				if chunk != nil {
					if p := pcmPeak(chunk); p > r.peak {
						r.mu.Lock()
						r.peak = p
						r.mu.Unlock()
					}
					select {
					case r.out <- chunk:
					default:
						// 消费端太慢时丢弃，避免阻塞采集线程——但必须记账。
						r.mu.Lock()
						r.dropped++
						r.mu.Unlock()
					}
				}
			}
		}
	}
}

// Stop 停止采集并释放设备。
func (r *Recorder) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	close(r.stopCh)
	r.mu.Unlock()
	r.wg.Wait()

	r.mu.Lock()
	r.closeLocked()
	r.mu.Unlock()
	close(r.out)
}

func (r *Recorder) closeLocked() {
	if r.handle == 0 {
		return
	}
	_, _, _ = procWaveInStop.Call(r.handle)
	_, _, _ = procWaveInReset.Call(r.handle)
	for _, hdr := range r.headers {
		if hdr != nil {
			_, _, _ = procWaveInUnprepareHeader.Call(r.handle, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
		}
	}
	_, _, _ = procWaveInClose.Call(r.handle)
	r.handle = 0
	r.headers = nil
	r.bufs = nil
}

// waveErrText 把 waveIn 错误码翻译成系统提示。
func waveErrText(ret uintptr) string {
	var buf [256]uint16
	procWaveInErrorText.Call(ret, uintptr(unsafe.Pointer(&buf[0])), 256)
	return windows.UTF16ToString(buf[:])
}

func pcmPeak(b []byte) float64 {
	if len(b) < 2 {
		return 0
	}
	var peak int16
	for i := 0; i+1 < len(b); i += 2 {
		v := int16(binary.LittleEndian.Uint16(b[i:]))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return float64(peak) / 32768
}

// EncodeWAV 把 PCM16 数据封装为 WAV。
func EncodeWAV(pcm []byte, sampleRate int) []byte {
	buf := make([]byte, 44+len(pcm))
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+len(pcm)))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], 1)
	binary.LittleEndian.PutUint32(buf[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buf[32:], 2)
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(len(pcm)))
	copy(buf[44:], pcm)
	return buf
}

// WriteWAV 把 PCM 写为 wav 文件。
func WriteWAV(w io.Writer, pcm []byte, sampleRate int) error {
	_, err := w.Write(EncodeWAV(pcm, sampleRate))
	return err
}

// syscall 占位，确保在非 Windows 平台编译时给出明确提示。
var _ = syscall.NewLazyDLL
