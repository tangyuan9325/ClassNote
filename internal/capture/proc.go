package capture

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
)

const (
	th32csSnapProcess = 0x00000002
	invalidHandle     = ^uintptr(0)
)

type processEntry32 struct {
	Size          uint32
	CntUsage      uint32
	ProcessID     uint32
	DefaultHeapID uintptr
	ModuleID      uint32
	Threads       uint32
	ParentPID     uint32
	PriClassBase  int32
	Flags         uint32
	ExeFile       [windows.MAX_PATH]uint16
}

// ProcInfo 描述一个进程。
type ProcInfo struct {
	Name string
	PID  uint32
	Path string
}

// Processes 通过 Toolhelp 快照枚举进程。
func Processes() []ProcInfo {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snap == 0 || snap == invalidHandle {
		return nil
	}
	defer procCloseHandle.Call(snap)

	var e processEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []ProcInfo
	ok, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&e)))
	for ok != 0 {
		name := syscall.UTF16ToString(e.ExeFile[:])
		if name != "" {
			out = append(out, ProcInfo{Name: name, PID: e.ProcessID, Path: processPath(e.ProcessID)})
		}
		e.Size = uint32(unsafe.Sizeof(e))
		ok, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&e)))
	}
	return out
}

func processPath(pid uint32) string {
	h, _, _ := procOpenProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	ret, _, _ := procQueryFullImgNmW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ret == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}

// FindProcessInfo 按可执行文件名查找进程（不区分大小写）。
func FindProcessInfo(names ...string) (ProcInfo, bool) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	for _, p := range Processes() {
		if want[strings.ToLower(p.Name)] {
			return p, true
		}
	}
	return ProcInfo{}, false
}

// FindProcessDir 返回进程所在目录。
func FindProcessDir(names ...string) string {
	if p, ok := FindProcessInfo(names...); ok && p.Path != "" {
		return filepath.Dir(p.Path)
	}
	return ""
}
