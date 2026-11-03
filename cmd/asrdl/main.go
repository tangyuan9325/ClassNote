package main

import (
	"fmt"
	"os"
	"time"

	"classnote/internal/asrmodel"
)

func main() {
	root := os.Args[1]
	asrmodel.SetRuntimeDir(root)
	last := time.Now()
	m := asrmodel.NewManager(func(p asrmodel.Progress) {
		if time.Since(last) < time.Second {
			return
		}
		last = time.Now()
		fmt.Printf("  %5.1f%%  %6.1f/%.1fMB  %5.2fMB/s  %s\n",
			float64(p.Done)*100/float64(p.Total),
			float64(p.Done)/(1<<20), float64(p.Total)/(1<<20),
			float64(p.Speed)/(1<<20), p.Source)
	})
	id := "tiny_q5_1"
	if len(os.Args) > 2 {
		id = os.Args[2]
	}
	t0 := time.Now()
	if err := m.Start(id, ""); err != nil {
		fmt.Println("启动失败:", err)
		os.Exit(1)
	}
	for i := 0; i < 600; i++ {
		p := m.Current()
		switch p.State {
		case "done":
			fmt.Printf("完成，耗时 %.1fs\n", time.Since(t0).Seconds())
			spec, _ := asrmodel.FindModel(id)
			fmt.Println("落盘:", asrmodel.LocalModelPath(spec), "存在:", asrmodel.HasModel(spec))
			return
		case "error":
			fmt.Println("失败:", p.Error)
			os.Exit(1)
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println("超时")
	os.Exit(1)
}
