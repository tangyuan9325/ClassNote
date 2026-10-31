package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/go-ole/go-ole"
)

// 通过 COM 控制正在放映的 PowerPoint：goto N / next / prev / first / exit
func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	defer ole.CoUninitialize()

	app := attach()
	if app == nil {
		fmt.Println("no PowerPoint")
		return
	}
	defer app.Release()
	wins, err := app.GetProperty("SlideShowWindows")
	if err != nil {
		fmt.Println("no slideshow:", err)
		return
	}
	ws, _ := wins.Value().(*ole.IDispatch)
	if ws == nil {
		fmt.Println("no SlideShowWindows")
		return
	}
	c, _ := ws.GetProperty("Count")
	if c.Val == 0 {
		fmt.Println("no active show")
		return
	}
	item, err := ws.CallMethod("Item", int32(1))
	if err != nil {
		fmt.Println("Item:", err)
		return
	}
	w, _ := item.Value().(*ole.IDispatch)
	view, err := w.GetProperty("View")
	if err != nil {
		fmt.Println("View:", err)
		return
	}
	v, _ := view.Value().(*ole.IDispatch)

	cmd := "next"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "goto":
		n, _ := strconv.Atoi(os.Args[2])
		if _, err := v.CallMethod("GotoSlide", int32(n)); err != nil {
			fmt.Println("GotoSlide:", err)
			return
		}
	case "next":
		_, _ = v.CallMethod("Next")
	case "prev":
		_, _ = v.CallMethod("Previous")
	case "first":
		_, _ = v.CallMethod("First")
	case "exit":
		_, _ = v.CallMethod("Exit")
	}
	time.Sleep(400 * time.Millisecond)
	sl, err := v.GetProperty("Slide")
	if err == nil {
		idx, _ := sl.Value().(*ole.IDispatch)
		if idx != nil {
			n, _ := idx.GetProperty("SlideIndex")
			fmt.Printf("%s ok, slide=%v\n", cmd, n.Value())
			return
		}
	}
	fmt.Println(cmd, "ok")
}

func attach() *ole.IDispatch {
	for _, pid := range []string{"PowerPoint.Application", "KWPP.Application"} {
		g, err := ole.CLSIDFromProgID(pid)
		if err != nil || g == nil {
			continue
		}
		unk, err := ole.GetActiveObject(g, ole.IID_IDispatch)
		if err != nil || unk == nil {
			continue
		}
		d, err := unk.QueryInterface(ole.IID_IDispatch)
		if err == nil {
			return d
		}
	}
	return nil
}
