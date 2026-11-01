// mictest 诊断 waveIn 打不开的原因：枚举设备、尝试不同采样率/设备 ID。
package main

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"classnote/internal/audio"
)

var (
	winmm            = windows.NewLazySystemDLL("winmm.dll")
	procGetNum       = winmm.NewProc("waveInGetNumDevs")
	procGetCaps      = winmm.NewProc("waveInGetDevCapsW")
	procOpen         = winmm.NewProc("waveInOpen")
	procClose        = winmm.NewProc("waveInClose")
	procGetErrorText = winmm.NewProc("waveInGetErrorTextW")
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

type waveInCapsW struct {
	ManufacturerID uint16
	ProductID      uint16
	DriverVersion  uint32
	PName          [32]uint16
	Formats        uint32
	Channels       uint16
	Reserved1      uint16
	Reserved2      uint32
}

func errText(code uintptr) string {
	var buf [256]uint16
	procGetErrorText.Call(code, uintptr(unsafe.Pointer(&buf[0])), 256)
	return windows.UTF16ToString(buf[:])
}

func main() {
	n, _, _ := procGetNum.Call()
	fmt.Printf("devices=%d\n", n)
	for i := 0; i < int(n); i++ {
		var c waveInCapsW
		ret, _, _ := procGetCaps.Call(uintptr(i), uintptr(unsafe.Pointer(&c)), unsafe.Sizeof(c))
		name := windows.UTF16ToString(c.PName[:])
		fmt.Printf("  [%d] ret=%d name=%q channels=%d formats=0x%x\n",
			i, ret, name, c.Channels, c.Formats)
	}
	rates := []uint32{16000, 22050, 44100, 48000}
	mapper := ^uintptr(0)
	fmt.Printf("WAVE_MAPPER probe\n")
	for _, rate := range rates {
		wf := waveFormatEx{FormatTag: 1, Channels: 1, SamplesPerSec: rate,
			AvgBytesPerSec: rate * 2, BlockAlign: 2, BitsPerSample: 16}
		var h uintptr
		ret, _, _ := procOpen.Call(uintptr(unsafe.Pointer(&h)), mapper,
			uintptr(unsafe.Pointer(&wf)), 0, 0, 0)
		fmt.Printf("  rate=%d mapper ret=%d (%s)\n", rate, ret, errText(ret))
		if ret == 0 {
			procClose.Call(h)
		}
	}
	for i := 0; i < int(n); i++ {
		for _, rate := range rates {
			wf := waveFormatEx{FormatTag: 1, Channels: 1, SamplesPerSec: rate,
				AvgBytesPerSec: rate * 2, BlockAlign: 2, BitsPerSample: 16}
			var h uintptr
			ret, _, _ := procOpen.Call(uintptr(unsafe.Pointer(&h)), uintptr(i),
				uintptr(unsafe.Pointer(&wf)), 0, 0, 0)
			if ret == 0 {
				fmt.Printf("  dev=%d rate=%d OK\n", i, rate)
				procClose.Call(h)
			} else {
				fmt.Printf("  dev=%d rate=%d ret=%d (%s)\n", i, rate, ret, errText(ret))
			}
		}
	}

	fmt.Println("audio.Recorder.Start(-1)")
	rec := audio.New(16000)
	if err := rec.Start(-1); err != nil {
		fmt.Println("  error:", err)
		return
	}
	fmt.Println("  OK, collecting 2s")
	deadline := time.After(2 * time.Second)
	peak := 0.0
	for {
		select {
		case <-deadline:
			rec.Stop()
			fmt.Printf("  peak=%.4f err=%v\n", peak, rec.Err())
			return
		case <-rec.Output():
			if p := rec.Peak(); p > peak {
				peak = p
			}
		}
	}
}
