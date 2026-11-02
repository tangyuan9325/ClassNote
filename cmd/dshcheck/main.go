// Command dshcheck 是 DSH 运行时的联调小工具（开发用，不参与发布）。
//
// 用法：
//
//	go run ./cmd/dshcheck -root <数据目录> [-plugin <npm 包名>]
//
// 它会依次：打印状态 → 启动 → 打印带令牌地址 → 可选装一个插件 → 停止。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"classnote/internal/dsh"
)

func main() {
	root := flag.String("root", "", "运行时数据目录")
	plugin := flag.String("plugin", "", "要安装的 npm 包名")
	keep := flag.Bool("keep", false, "跑完不停机，方便手工点开看看")
	force := flag.Bool("force", false, "不兼容也强行安装")
	install := flag.Bool("install", false, "从零安装运行时")
	flag.Parse()
	if *root == "" {
		fmt.Println("需要 -root")
		os.Exit(2)
	}
	rt := dsh.New(*root, func(line string) { fmt.Println("  |", line) })

	st := rt.Status()
	fmt.Printf("阶段=%s 已装=%v 运行=%v Node=%s(%s) 插件=%v\n",
		st.Phase, st.Installed, st.Running, st.NodeVer, st.NodePath, st.Plugins)
	if st.Error != "" {
		fmt.Println("提示:", st.Error)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// 无论成功失败都要停掉子进程，否则会留下一个占着端口的孤儿。
	defer rt.Stop()

	if *install {
		t0 := time.Now()
		if err := rt.Install(ctx); err != nil {
			fmt.Println("安装失败:", err)
			os.Exit(1)
		}
		fmt.Printf("安装完成，耗时 %v\n", time.Since(t0).Round(time.Second))
	}
	t0 := time.Now()
	if err := rt.Start(ctx); err != nil {
		fmt.Println("启动失败:", err)
		os.Exit(1)
	}
	fmt.Printf("启动耗时 %v\n地址: %s\n", time.Since(t0).Round(time.Millisecond), rt.URL())

	if *plugin != "" {
		if err := rt.AddPlugin(ctx, *plugin, *force); err != nil {
			fmt.Println("装插件失败:", err)
			os.Exit(1)
		}
		fmt.Println("装完后的插件列表:", rt.Plugins())
	}
	if *keep {
		fmt.Println("保持运行，Ctrl+C 退出")
		select {}
	}
	rt.Stop()
	fmt.Println("已停止")
}
