// ClassNote —— 课堂智能笔记助手
//
// 一个面向 Windows 教学场景的本地课堂助手：
// 自动识别幻灯片放映、按页截图、实时语音转写、AI 逐页笔记与课堂报告。
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"classnote/internal/app"
	"classnote/internal/appinfo"
	"classnote/internal/asr"
	"classnote/internal/capture"
	"classnote/internal/config"
	"classnote/internal/integration"
	"classnote/internal/lesson"
	"classnote/internal/logx"
	"classnote/internal/paths"
	"classnote/internal/server"
	"classnote/internal/store"
	"classnote/internal/update"
)

//go:embed all:web
var webAssets embed.FS

var (
	version   = "1.8.3"
	buildTime = "dev"
)

// windowTitle 与页面 <title> 一致，用于把已有窗口唤到前台。
const windowTitle = "ClassNote · 课记"

func main() {
	// 辅助升级进程：主程序把自己复制成 updater-*.exe，再带这个参数启动它。
	// 必须在 flag.Parse 之前拦下，否则它会因为不认识这个参数而直接退出。
	if plan, ok := update.IsUpdater(os.Args[1:]); ok {
		os.Exit(update.RunUpdater(plan))
	}

	port := flag.Int("port", 41735, "本地服务起始端口")
	noWindow := flag.Bool("no-window", false, "只启动服务，不打开应用窗口")
	noTray := flag.Bool("no-tray", false, "不显示系统托盘图标")
	openBrowser := flag.Bool("browser", false, "用默认浏览器打开界面")
	verbose := flag.Bool("v", false, "输出日志到控制台")
	reset := flag.Bool("reset", false, "重置配置（保留课堂记录）")
	flag.Parse()

	appinfo.Version = version
	appinfo.BuildTime = buildTime

	paths := paths.Resolve()
	if err := paths.Ensure(); err != nil {
		fmt.Fprintf(os.Stderr, "创建数据目录失败: %v\n", err)
		os.Exit(1)
	}
	if err := logx.Setup(paths.Logs); err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
	}
	if *verbose {
		logx.EnableStdout()
	}
	defer logx.Close()
	capture.InitDPI()

	logx.Infof("main", "ClassNote %s (%s) 启动，工作目录 %s", version, buildTime, paths.Root)

	if *reset {
		_ = os.Remove(paths.Config)
		logx.Infof("main", "已重置配置")
	}

	// 单实例：已有服务在跑时，把它的窗口唤到前台（没有则新开一个）。
	if app.IsRunning(fmt.Sprintf("http://127.0.0.1:%d", *port)) {
		url := fmt.Sprintf("http://127.0.0.1:%d", *port)
		logx.Infof("main", "检测到已运行的实例，唤醒界面")
		if !*noWindow {
			if h, ok := app.FindWindowByTitle(windowTitle); ok {
				app.FocusWindow(h)
			} else {
				_ = app.LaunchWindow(url, filepath.Join(paths.Root, "data", "uiprofile"), 1380, 900)
			}
		}
		return
	}

	cfgStore, err := config.Load(paths.Config)
	if err != nil {
		logx.Errorf("main", "读取配置失败: %v", err)
	}
	cfg := cfgStore.Get()

	st := store.New(paths.Sessions)
	engine := lesson.New(paths, st, cfg)
	// 本地语音模型：运行时目录要先定下来（可能是 %ProgramData% 下的
	// ASCII 副本），下载器与 whisper-server 都得往同一个地方找。
	// 真正的启动 / 首次下载交给 srv.BootstrapASR()，那里才能推 SSE 进度。
	wdir := asr.LocalDefaultDir(paths.Root)
	if asciiDir, err := asr.EnsureASCIIWhisperDir(wdir); err == nil {
		wdir = asciiDir
	} else {
		logx.Warnf("main", "语音模型目录准备失败: %v", err)
	}
	asr.SetRuntimeDir(wdir)

	ci := integration.NewClassIsland(cfg.Integ.ClassIsland, cfg.Lesson.Subjects, cfg.Lesson.ManualTimetable)
	// 首次通过进程探测到 ClassIsland 数据目录后写回配置，
	// 这样下次 ClassIsland 没开也能读取课表、识别插件。
	if dir := ci.DataDir(); dir != "" && cfg.Integ.ClassIsland.DataDir == "" {
		if _, err := cfgStore.Update(func(c *config.Config) error {
			c.Integ.ClassIsland.DataDir = dir
			return nil
		}); err == nil {
			cfg.Integ.ClassIsland.DataDir = dir
			ci.Update(cfg.Integ.ClassIsland, cfg.Lesson.Subjects, cfg.Lesson.ManualTimetable)
			logx.Infof("classisland", "已记录数据目录 %s", dir)
		}
	}
	icc := integration.NewICCBridge(cfg.Integ.ICC)

	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		logx.Errorf("main", "加载界面资源失败: %v", err)
		os.Exit(1)
	}
	srv := server.New(paths, cfgStore, st, engine, ci, icc, sub)
	// 有模型就直接起服务；没有就后台下载默认轻量模型（不阻塞启动）。
	go srv.BootstrapASR()
	if _, cerr := srv.RefreshClient(); cerr != nil {
		logx.Warnf("main", "模型尚未就绪: %v", cerr)
	}
	p, err := srv.Listen(*port)
	if err != nil {
		logx.Errorf("main", "启动服务失败: %v", err)
		os.Exit(1)
	}
	srv.WriteRuntime()
	url := fmt.Sprintf("http://127.0.0.1:%d", p)
	logx.Infof("main", "服务地址 %s", url)

	// 用网页版 AI 时，配置完成后就把每门课的固定对话准备好：
	// 之后整堂课每页只发材料，不再重复上千字的系统提示。
	// 延后几秒再跑，别和界面启动抢时间；失败也不影响使用（分析前会按需重建）。
	if boot := cfgStore.Get(); boot.Provider.Mode == "web" && !boot.WebAI.NoPrimeOnStart {
		go func() {
			time.Sleep(6 * time.Second)
			srv.PrimeChats(false)
		}()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.NewMonitor(engine, ci, icc, paths.Root, cfgStore.Get).Run(ctx)

	var (
		tray         *app.Tray
		shutdownOnce sync.Once
	)

	// 打开界面：已有窗口就前置，否则新建。
	openUI := func(panel string) {
		if h, ok := app.FindWindowByTitle(windowTitle); ok {
			app.FocusWindow(h)
			if panel != "" {
				engine.EmitExternal("ui_command", map[string]string{"action": panel})
			}
			return
		}
		target := url
		if panel != "" {
			target = url + "/?panel=" + panel
		}
		if err := app.LaunchWindow(target, filepath.Join(paths.Root, "data", "uiprofile"), 1380, 900); err != nil {
			logx.Warnf("main", "打开窗口失败: %v，改用默认浏览器", err)
			_ = app.OpenExternal(target)
		}
	}

	toggle := func() string {
		s := engine.Status()
		if s.Running {
			go func() {
				if err := engine.StopAndReport(); err != nil {
					logx.Warnf("tray", "结束课堂失败: %v", err)
					if tray != nil {
						tray.Notify("课记", "结束失败："+err.Error())
					}
					return
				}
			}()
			return "正在结束本堂课并生成报告…"
		}
		subject := "课堂"
		if s := ci.State(); s.Current != nil && s.Current.Subject != "" {
			subject = s.Current.Subject
		}
		if err := engine.Start(subject, "", "tray"); err != nil {
			return "开始失败：" + err.Error()
		}
		return "已开始记录：" + subject
	}

	// forceQuit 不做二次确认：调用它的两条路径（升级安装、/api/shutdown）
	// 已经各自确认过了。它替代 os.Exit，顺带停掉托盘、语音服务与 DSH——
	// 直接 Exit 会把 whisper-server 留在机器上占着端口，下次启动就抢不到。
	forceQuit := func(reason string) {
		shutdownOnce.Do(func() {
			logx.Infof("main", "退出程序：%s", reason)
			if tray != nil {
				tray.Stop()
			}
			if engine.Status().Running {
				done := make(chan struct{})
				go func() { _ = engine.Stop(); close(done) }()
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					logx.Warnf("main", "结束课堂超时，直接退出")
				}
			}
			cancel()
			srv.Shutdown()
			os.Exit(0)
		})
	}

	quit := func() {
		if !app.AskQuit(0, engine.Status().Running) {
			return
		}
		forceQuit("用户请求退出")
	}

	if !*noTray {
		tray, err = app.NewTray(filepath.Join(paths.Root, "data"), app.TrayActions{
			Open:    openUI,
			Toggle:  toggle,
			Reports: func() { _ = app.OpenFolder(paths.Reports) },
			Data:    func() { _ = app.OpenFolder(paths.Root) },
			Quit:    quit,
			Status: func() app.TrayStatus {
				s := engine.Status()
				return app.TrayStatus{Recording: s.Running, Subject: s.Subject, Elapsed: s.ElapsedSec}
			},
		})
		if err != nil {
			logx.Warnf("main", "初始化托盘失败: %v", err)
		} else if err := tray.Start(); err != nil {
			logx.Warnf("main", "启动托盘失败: %v", err)
			tray = nil
		} else {
			logx.Infof("main", "托盘图标已就绪")
			// 首次运行提示：Windows 默认把新图标收进“隐藏的图标”。
			hint := filepath.Join(paths.Root, "data", "tray-hint-shown")
			if _, statErr := os.Stat(hint); statErr != nil {
				_ = os.WriteFile(hint, []byte(time.Now().Format(time.RFC3339)), 0o644)
				go func() {
					time.Sleep(1600 * time.Millisecond)
					tray.Notify("课记已在托盘运行",
						"没看到图标？点任务栏的 ^ 展开隐藏图标，把它拖到任务栏上即可固定。")
				}()
			}
		}
	}

	// 关闭接口与升级安装共用同一条退出路径：都会走到 forceQuit。
	srv.SetExitHook(func() { forceQuit("升级或关闭接口") })

	// 上一次升级的结果：成功就报个平安，失败就把原因摆出来。
	// 越过这一步，用户对「点了安装之后程序消失又出现」只会觉得莫名其妙。
	if res, ok := update.LastResult(paths.Root); ok && time.Since(res.At) < 6*time.Hour {
		go func() {
			time.Sleep(2 * time.Second)
			if tray == nil {
				return
			}
			if res.OK {
				msg := fmt.Sprintf("已更新到 %s", res.To)
				if !res.Restarted {
					msg += "，请手动重新打开"
				}
				tray.Notify("更新完成", msg)
				return
			}
			tray.Notify("上次更新失败", res.Error+"（已恢复到旧版本）")
		}()
	}

	if !*noWindow {
		openUI("")
	} else if *openBrowser {
		_ = app.OpenExternal(url)
	}

	// 桌面悬浮球：常驻角落，显示课堂状态，点开主界面。
	var ball *app.FloatBall
	if cfg.UI.ShowFloatBall {
		ball = app.NewFloatBall(app.FloatBallOptions{
			OpenMain: func() { openUI("") },
			OpenMenu: func() {
				if tray != nil {
					tray.Notify("课记", "右键托盘图标可以开始/结束记录")
				}
			},
			Status: func() app.FloatBallStatus {
				s := engine.Status()
				return app.FloatBallStatus{
					Running: s.Running, Subject: s.Subject,
					Elapsed: s.ElapsedSec, Level: s.Level,
				}
			},
		})
		if err := ball.Start(); err != nil {
			logx.Warnf("main", "悬浮球启动失败: %v", err)
		} else {
			logx.Infof("main", "桌面悬浮球已就绪")
		}
	}

	// 托盘状态跟随引擎，事件变化时给出气泡提示。
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		first := true
		wasRunning := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s := engine.Status()
				if tray != nil {
					tray.SetStatus(app.TrayStatus{Recording: s.Running, Subject: s.Subject, Elapsed: s.ElapsedSec})
				}
				if first {
					first = false
					wasRunning = s.Running
					continue
				}
				if ball != nil {
					// 老师全屏放课件时，置顶的小球会压住板书/课件角，先让路。
					presenting := engine.Detector().Status().Presenting
					ball.SetVisible(!presenting)
					ball.SetStatus(app.FloatBallStatus{Running: s.Running, Subject: s.Subject, Elapsed: s.ElapsedSec, Level: s.Level})
				}
				if s.Running != wasRunning {
					wasRunning = s.Running
					if tray == nil {
						continue
					}
					if s.Running {
						tray.Notify("开始记录", "科目："+s.Subject)
					} else {
						tray.Notify("课堂已结束", "笔记与报告已保存")
					}
				}
			}
		}
	}()

	// 启动时检查更新（可选），有新版就用托盘提示。
	if cfg.GitHub.CheckUpdates {
		go func() {
			time.Sleep(20 * time.Second) // 等网络与托盘就绪
			info := update.Check(cfg.GitHub.Repo, cfg.GitHub.Token)
			if info.HasUpdate {
				logx.Infof("main", "发现新版本 %s（当前 %s）", info.Latest, info.Current)
				// 正在上课就别弹气泡：一条「有新版本」的提示不该出现在投影上。
				if tray != nil && !engine.Status().Running {
					tray.Notify("有新版本", fmt.Sprintf("ClassNote %s 可用（当前 %s），到 GitHub 下载", info.Latest, info.Current))
				}
			} else if info.Error != "" {
				logx.Debugf("main", "更新检查：%s", info.Error)
			}
		}()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	logx.Infof("main", "收到退出信号，正在关闭")
	if tray != nil {
		tray.Stop()
	}
	cancel()
	time.Sleep(300 * time.Millisecond)
}
