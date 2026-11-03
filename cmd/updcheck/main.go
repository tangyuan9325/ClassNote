// Command updcheck 是自动更新的联调工具。
//
// 用法：
//
//	go run ./cmd/updcheck -repo tangyuan9325/ClassNote        # 只查版本
//	go run ./cmd/updcheck -repo ... -nodes                    # 查版本 + 节点测速
//	go run ./cmd/updcheck -repo ... -dl /tmp/upd              # 再真下一个包
//	go run ./cmd/updcheck -apply /tmp/upd/xxx.zip -root /tmp/fakeroot
//
// 在 Windows 上跑最后一条会真的替换 /tmp/fakeroot 里的文件并尝试重启，
// 用来验证「辅助进程 + 备份 + 回滚」这一整套；在 Linux 上则只能验证到
// 文件替换与结果落盘（PE 文件跑不起来，退出码 4）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"classnote/internal/appinfo"
	"classnote/internal/update"
)

func main() {
	// 与主程序一样：先拦升级助手模式，否则参数解析会报错。
	if plan, ok := update.IsUpdater(os.Args[1:]); ok {
		os.Exit(update.RunUpdater(plan))
	}

	repo := flag.String("repo", "tangyuan9325/ClassNote", "owner/name")
	token := flag.String("token", "", "GitHub Token（私有仓库）")
	nodes := flag.Bool("nodes", false, "对更新包做一次节点测速")
	dl := flag.String("dl", "", "把更新包下载到这个目录")
	apply := flag.String("apply", "", "安装指定的 zip（需要 -root）")
	root := flag.String("root", "", "安装目录（配合 -apply）")
	version := flag.String("version", "9.9.9", "安装时写入的版本号")
	flag.Parse()

	if *apply != "" {
		if *root == "" {
			fmt.Fprintln(os.Stderr, "-apply 需要同时给 -root")
			os.Exit(2)
		}
		info, err := update.Apply(*apply, *root, *version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "启动升级失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("升级助手已就绪：PID %d，%d 个文件，日志 %s\n", info.HelperPID, info.Files, info.Log)
		fmt.Println("主程序（本进程）现在退出，助手会在 1 秒后接手…")
		return
	}

	fmt.Printf("当前版本 %s\n", appinfo.Version)
	info := update.Check(*repo, *token)
	if info.Error != "" {
		fmt.Fprintf(os.Stderr, "检查失败: %s\n", info.Error)
		os.Exit(1)
	}
	fmt.Printf("最新版本 %s（检查方式 %s，发布 %s）\n", info.Latest, info.Source, info.PublishedAt)
	if !info.HasUpdate {
		fmt.Println("已是最新")
	}
	for _, a := range info.Assets {
		fmt.Printf("  附件 %s  %d 字节\n", a.Name, a.Size)
	}
	if len(info.Assets) == 0 {
		fmt.Println("没有可下载的附件")
		return
	}

	asset := info.Assets[0]
	if *nodes {
		list := update.TestNodes(context.Background(), update.ProbeReq{URL: asset.URL, Custom: nil, DirectOnly: *token != "", Force: true})
		fmt.Println("节点测速：")
		for _, n := range list {
			if !n.Tested {
				continue
			}
			if n.OK {
				fmt.Printf("  %-22s %8.1f KB/s  首字 %4d ms  总大小 %d\n", n.Name, float64(n.Speed)/1024, n.Latency, n.Size)
			} else {
				fmt.Printf("  %-22s 不可用：%s\n", n.Name, n.Error)
			}
		}
	}

	if *dl != "" {
		if err := os.MkdirAll(*dl, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "创建目录失败: %v\n", err)
			os.Exit(1)
		}
		d := update.NewDownloader(func(p update.Progress) {
			if p.State == "downloading" || p.State == "probing" || p.State == "verifying" {
				fmt.Printf("\r  %-10s %6.1f%%  %8.1f KB/s  %-20s", p.State,
					percent(p.Done, p.Total), float64(p.Speed)/1024, p.Node)
			}
		})
		eps := update.Endpoints(asset.URL, update.TestNodes(context.Background(), update.ProbeReq{
			URL: asset.URL, DirectOnly: *token != "", Force: true,
		}))
		if err := d.Start(update.Job{
			URL: asset.URL, Name: asset.Name, Dir: *dl,
			Token: *token, Expect: asset.Size, Endpoints: eps,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "\n启动下载失败: %v\n", err)
			os.Exit(1)
		}
		for {
			p := d.Current()
			if p.State == "done" || p.State == "error" || p.State == "canceled" {
				fmt.Printf("\r  %-10s %6.1f%%  %-20s\n", p.State, percent(p.Done, p.Total), p.Node)
				if p.State != "done" {
					fmt.Fprintf(os.Stderr, "下载失败: %s\n", p.Error)
					os.Exit(1)
				}
				fmt.Println("已保存到 " + p.Path)
				break
			}
			sleep()
		}
	}

}

func sleep() { time.Sleep(300 * time.Millisecond) }

func percent(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}
