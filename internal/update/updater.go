package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ApplyFlag 是辅助升级进程的命令行开关。
//
// 主程序启动时如果看到它，就只做升级、不开界面。
// 用同一个 exe 当助手的好处是：不需要额外分发一个二进制，
// 也不会出现「助手版本和主程序版本对不上」的诡异问题。
const ApplyFlag = "--apply-update"

// IsUpdater 判断本次启动是否是辅助升级进程，返回计划文件路径。
func IsUpdater(args []string) (string, bool) {
	for i, a := range args {
		if strings.HasPrefix(a, ApplyFlag+"=") {
			return strings.TrimSpace(strings.TrimPrefix(a, ApplyFlag+"=")), true
		}
		if a == ApplyFlag && i+1 < len(args) {
			return strings.TrimSpace(args[i+1]), true
		}
	}
	return "", false
}

// RunUpdater 执行升级流程，返回进程退出码。
//
// 步骤：等主程序退出 → 备份旧文件 → 覆盖 → 写结果 → 重启。
// 任一步失败都会回滚，并把原因写进结果文件——用户下次打开程序
// 就能看到「上次更新失败：xxx，已恢复到旧版本」，而不是对着一个
// 打不开的 exe 发呆。
func RunUpdater(planPath string) int {
	plan, err := readPlan(planPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取升级计划失败: %v\n", err)
		return 2
	}
	lg := newUpdLog(plan.Log)
	defer lg.close()
	// 这行是握手标记：主程序看到 READY 才敢退出。
	lg.printf("READY 升级助手已启动（PID %d），版本 %s → %s", os.Getpid(), plan.From, plan.Version)

	if plan.OldPID > 0 {
		lg.printf("等待主程序（PID %d）退出…", plan.OldPID)
		wait := time.Duration(plan.WaitMS) * time.Millisecond
		if wait <= 0 {
			wait = 90 * time.Second
		}
		if !waitExit(plan.OldPID, wait) {
			return updaterFail(lg, plan, "主程序没有按时退出，本次更新已取消（文件未改动）")
		}
		// 进程刚退出时文件句柄可能还没完全释放，稍等一下。
		time.Sleep(500 * time.Millisecond)
		lg.printf("主程序已退出")
	}

	n, err := swap(&plan, lg)
	if err != nil {
		return updaterFail(lg, plan, err.Error())
	}

	res := Result{
		OK: true, From: plan.From, To: plan.Version,
		At: time.Now(), Files: n, Log: plan.Log,
	}
	lg.printf("文件替换完成，共 %d 个文件", n)

	exe := filepath.Join(plan.Root, "ClassNote.exe")
	if err := relaunch(exe, plan.Args, plan.Root); err != nil {
		res.Restarted = false
		res.Error = "更新成功，但自动重启失败：" + err.Error()
		lg.printf("重启失败：%v", err)
	} else {
		res.Restarted = true
		lg.printf("已重新启动 %s", exe)
	}
	_ = writeJSON(plan.Result, res)

	// 清理现场：备份和暂存目录都不该留在用户的磁盘上。
	// 重启成功后等几秒再删，避免刚起来的程序还在读旧目录。
	time.Sleep(2 * time.Second)
	if err := os.RemoveAll(plan.Backup); err != nil {
		lg.printf("清理备份失败（不影响使用）：%v", err)
	}
	if err := os.RemoveAll(plan.Staging); err != nil {
		lg.printf("清理暂存目录失败（不影响使用）：%v", err)
	}
	_ = os.Remove(planPath)
	lg.printf("升级结束")
	// 自己删自己：Windows 上运行中的 exe 删不掉，交给一个短命的 cmd
	// 在进程退出后再删；不删的话 data/updates 里会攒下一堆 8MB 的副本。
	removeSelf()
	if !res.Restarted {
		return 4
	}
	return 0
}

// updaterFail 写失败结果、尽量把旧版本拉起来，然后返回非零退出码。
func updaterFail(lg *updLog, plan Plan, msg string) int {
	lg.printf("升级失败：%s", msg)
	_ = writeJSON(plan.Result, Result{
		OK: false, From: plan.From, To: plan.Version,
		At: time.Now(), Error: msg, Log: plan.Log,
		RolledBack: true,
	})
	exe := filepath.Join(plan.Root, "ClassNote.exe")
	if err := relaunch(exe, plan.Args, plan.Root); err != nil {
		lg.printf("恢复启动失败：%v", err)
		return 5
	}
	return 3
}

func readPlan(path string) (Plan, error) {
	var p Plan
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.Root == "" || p.Payload == "" {
		return p, fmt.Errorf("升级计划不完整")
	}
	return p, nil
}

type movedFile struct {
	backup string // 备份位置
	target string // 原位置
}

// swap 把 payload 覆盖到安装目录，失败时回滚。
func swap(plan *Plan, lg *updLog) (int, error) {
	if err := verifyPayload(plan.Payload); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(plan.Backup, 0o755); err != nil {
		return 0, fmt.Errorf("创建备份目录失败: %w", err)
	}

	var (
		backed  []movedFile
		created []string
		count   int
	)
	rollback := func() error {
		var firstErr error
		for _, f := range created {
			// 新增文件删不掉不是致命问题（比如父路径本来就是个文件），
			// 真正要保证的是备份能回得去。
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) && firstErr == nil {
				firstErr = err
			}
		}
		for _, m := range backed {
			_ = os.MkdirAll(filepath.Dir(m.target), 0o755)
			_ = os.Remove(m.target)
			if err := os.Rename(m.backup, m.target); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	walkErr := filepath.Walk(plan.Payload, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(plan.Payload, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." || info.IsDir() {
			return nil
		}
		if skipInPayload(rel) {
			return nil
		}
		dst := filepath.Join(plan.Root, rel)
		// 大模型文件（几十上百 MB）内容没变就别重复写：写一次要几秒，
		// 而且失败概率比小文件高得多。按体积判断足够可靠——模型是按
		// 体积命名的，同一个体积就是同一个文件。
		if sameBigFile(dst, info) {
			return nil
		}
		if _, serr := os.Stat(dst); serr == nil {
			bk := filepath.Join(plan.Backup, rel)
			_ = os.MkdirAll(filepath.Dir(bk), 0o755)
			if rerr := os.Rename(dst, bk); rerr != nil {
				// 跨盘或被占用：退回复制 + 删除。
				if cerr := copyFile(dst, bk); cerr != nil {
					return fmt.Errorf("备份 %s 失败: %w", rel, rerr)
				}
				_ = os.Remove(dst)
			}
			backed = append(backed, movedFile{backup: bk, target: dst})
		} else {
			created = append(created, dst)
		}
		if err := copyFile(p, dst); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", rel, err)
		}
		count++
		return nil
	})
	if walkErr != nil {
		lg.printf("覆盖文件出错：%v，开始回滚…", walkErr)
		if rerr := rollback(); rerr != nil {
			lg.printf("回滚未完全成功：%v", rerr)
			return 0, fmt.Errorf("%v（回滚时又出错：%v，请手动重新解压发布包）", walkErr, rerr)
		}
		lg.printf("已回滚到旧版本")
		return 0, fmt.Errorf("%v（已自动回滚到旧版本）", walkErr)
	}

	// 最后再确认一次：新 exe 真的在位。
	exe := filepath.Join(plan.Root, "ClassNote.exe")
	if fi, err := os.Stat(exe); err != nil || fi.Size() < 1<<20 {
		lg.printf("覆盖后发现 ClassNote.exe 异常，开始回滚…")
		_ = rollback()
		return 0, fmt.Errorf("覆盖后 ClassNote.exe 异常（%v），已回滚", err)
	}
	return count, nil
}

// skipInPayload 判断某个相对路径不该被更新包覆盖。
//
// 数据目录与配置是用户的课堂记录、密钥和课表，任何时候都不能被
// 更新包里的同名（通常是空的）目录盖掉。
func skipInPayload(rel string) bool {
	r := strings.ToLower(filepath.ToSlash(rel))
	if r == "config.json" || r == "data" || strings.HasPrefix(r, "data/") {
		return true
	}
	if strings.HasPrefix(r, "update-staging") || strings.HasPrefix(r, "update.cmd") || strings.HasPrefix(r, "update.cmd.bak") {
		return true
	}
	if !strings.Contains(r, "/") && strings.HasSuffix(r, ".log") {
		return true
	}
	return false
}

// sameBigFile 判断安装目录里已有的大文件是否可以跳过。
func sameBigFile(dst string, src os.FileInfo) bool {
	if src.Size() < 8<<20 {
		return false
	}
	if !strings.HasSuffix(strings.ToLower(src.Name()), ".bin") {
		return false
	}
	di, err := os.Stat(dst)
	if err != nil {
		return false
	}
	return di.Size() == src.Size()
}
