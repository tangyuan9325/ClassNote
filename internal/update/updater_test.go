package update

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExe 造一个「像样的」Windows 程序：MZ 头 + 1MB 体积。
func fakeExe(seed byte) []byte {
	b := make([]byte, 1<<20)
	b[0], b[1] = 'M', 'Z'
	for i := 2; i < len(b); i++ {
		b[i] = seed
	}
	return b
}

func TestStageRejectsBadPayloads(t *testing.T) {
	root := t.TempDir()

	// 1. 没有 ClassNote.exe
	p1 := makeZip(t, map[string][]byte{"readme.txt": []byte("hi")})
	if _, _, _, err := Stage(p1, root); err == nil || !strings.Contains(err.Error(), "ClassNote.exe") {
		t.Fatalf("缺 exe 应报错，实际 %v", err)
	}
	// 2. exe 是个空壳
	p2 := makeZip(t, map[string][]byte{"ClassNote.exe": []byte("MZ")})
	if _, _, _, err := Stage(p2, root); err == nil {
		t.Fatal("体积不对的 exe 应报错")
	}
	// 3. exe 不是 PE
	p3 := makeZip(t, map[string][]byte{"ClassNote.exe": append([]byte("PK"), bytes.Repeat([]byte("x"), 1<<20)...)})
	if _, _, _, err := Stage(p3, root); err == nil {
		t.Fatal("非 PE 文件应报错")
	}
	// 失败后不能把半成品留在磁盘上
	if _, err := os.Stat(filepath.Join(Dir(root), "staging")); !os.IsNotExist(err) {
		t.Fatal("校验失败后应清掉 staging")
	}
}

func TestStageUnwrapsVersionDir(t *testing.T) {
	root := t.TempDir()
	pkg := makeZip(t, map[string][]byte{
		"ClassNote-1.8.2/ClassNote.exe":  fakeExe(1),
		"ClassNote-1.8.2/web/index.html": []byte("<html></html>"),
	})
	payload, staging, n, err := Stage(pkg, root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(payload) != "ClassNote-1.8.2" {
		t.Fatalf("应取到内层目录，实际 %s", payload)
	}
	if n != 2 {
		t.Fatalf("文件数应为 2，实际 %d", n)
	}
	if staging == payload {
		t.Fatal("套了一层目录时，暂存根目录应该是内容目录的父目录")
	}
}

func TestSwapKeepsUserDataAndRollsBack(t *testing.T) {
	root := t.TempDir()
	oldExe := fakeExe(9)
	writeFile(t, filepath.Join(root, "ClassNote.exe"), oldExe)
	writeFile(t, filepath.Join(root, "config.json"), []byte(`{"keep":true}`))
	writeFile(t, filepath.Join(root, "data", "sessions", "s1.json"), []byte(`{"id":"s1"}`))
	writeFile(t, filepath.Join(root, "web", "index.html"), []byte("old"))

	payload := filepath.Join(t.TempDir(), "payload")
	newExe := fakeExe(7)
	writeFile(t, filepath.Join(payload, "ClassNote.exe"), newExe)
	writeFile(t, filepath.Join(payload, "config.json"), []byte(`{"keep":false}`))
	writeFile(t, filepath.Join(payload, "data", "sessions", "s1.json"), []byte(`{}`))
	writeFile(t, filepath.Join(payload, "web", "index.html"), []byte("new"))

	plan := &Plan{Root: root, Payload: payload, Backup: filepath.Join(root, "bk")}
	lg := newUpdLog(filepath.Join(root, "u.log"))
	defer lg.close()
	n, err := swap(plan, lg)
	if err != nil {
		t.Fatalf("升级应成功：%v", err)
	}
	if n != 2 { // exe 与 web/index.html；config.json 与 data/ 被跳过
		t.Fatalf("应覆盖 2 个文件，实际 %d", n)
	}
	if got := readFile(t, filepath.Join(root, "ClassNote.exe")); !bytes.Equal(got, newExe) {
		t.Fatal("exe 没有被替换")
	}
	if got := readFile(t, filepath.Join(root, "config.json")); string(got) != `{"keep":true}` {
		t.Fatalf("配置被更新包覆盖了：%s", got)
	}
	if got := readFile(t, filepath.Join(root, "data", "sessions", "s1.json")); string(got) != `{"id":"s1"}` {
		t.Fatalf("课堂记录被覆盖了：%s", got)
	}
	if got := readFile(t, filepath.Join(root, "web", "index.html")); string(got) != "new" {
		t.Fatalf("静态资源没更新：%s", got)
	}
	if got := readFile(t, filepath.Join(root, "bk", "ClassNote.exe")); !bytes.Equal(got, oldExe) {
		t.Fatal("旧 exe 应被备份下来")
	}
}

func TestSwapRollbackOnWriteFailure(t *testing.T) {
	root := t.TempDir()
	oldExe := fakeExe(3)
	writeFile(t, filepath.Join(root, "ClassNote.exe"), oldExe)
	// 安装目录里 web 是个文件；更新包里 web 是目录 → 建目录失败 → 触发回滚。
	writeFile(t, filepath.Join(root, "web"), []byte("i am a file"))

	payload := filepath.Join(t.TempDir(), "payload")
	writeFile(t, filepath.Join(payload, "ClassNote.exe"), fakeExe(4))
	writeFile(t, filepath.Join(payload, "web", "index.html"), []byte("new"))

	plan := &Plan{Root: root, Payload: payload, Backup: filepath.Join(root, "bk")}
	lg := newUpdLog(filepath.Join(root, "u.log"))
	defer lg.close()
	if _, err := swap(plan, lg); err == nil {
		t.Fatal("写入失败应报错")
	}
	if got := readFile(t, filepath.Join(root, "ClassNote.exe")); !bytes.Equal(got, oldExe) {
		t.Fatal("失败后必须回滚到旧 exe")
	}
}

func TestSameBigFileSkipsUnchangedModel(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ggml-base.bin")
	big := bytes.Repeat([]byte("m"), 9<<20)
	writeFile(t, p, big)
	fi, _ := os.Stat(p)
	if !sameBigFile(p, fi) {
		t.Fatal("同体积的大模型文件应跳过")
	}
	small := filepath.Join(dir, "readme.bin")
	writeFile(t, small, []byte("x"))
	si, _ := os.Stat(small)
	if sameBigFile(small, si) {
		t.Fatal("小文件不该跳过")
	}
}

func TestIsUpdaterAndRestartArgs(t *testing.T) {
	if got, ok := IsUpdater([]string{"--port", "1"}); ok || got != "" {
		t.Fatalf("普通启动不该被判成升级助手：%q %v", got, ok)
	}
	if got, ok := IsUpdater([]string{ApplyFlag, "/tmp/plan.json"}); !ok || got != "/tmp/plan.json" {
		t.Fatalf("空格形式解析失败：%q %v", got, ok)
	}
	if got, ok := IsUpdater([]string{ApplyFlag + "=/tmp/p2.json"}); !ok || got != "/tmp/p2.json" {
		t.Fatalf("等号形式解析失败：%q %v", got, ok)
	}
	args := restartArgs([]string{"--port", "5000", ApplyFlag, "/tmp/p.json", "--no-tray"})
	want := []string{"--port", "5000", "--no-tray"}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("重启参数应去掉升级开关，实际 %v", args)
	}
}

// TestRunUpdaterAbortsWhenAppStillRuns 是最重要的一条：
// 主程序没退出就绝不能动文件，否则会把正在运行的安装目录写坏。
func TestRunUpdaterAbortsWhenAppStillRuns(t *testing.T) {
	root := t.TempDir()
	oldExe := fakeExe(5)
	writeFile(t, filepath.Join(root, "ClassNote.exe"), oldExe)
	payload := filepath.Join(t.TempDir(), "payload")
	writeFile(t, filepath.Join(payload, "ClassNote.exe"), fakeExe(6))

	plan := Plan{
		Root: root, Payload: payload,
		Backup: filepath.Join(root, "bk"),
		Log:    filepath.Join(root, "u.log"),
		Result: ResultPath(root),
		OldPID: os.Getpid(), // 自己还活着，模拟「主程序没退」
		WaitMS: 300,
		From:   "1.8.1", Version: "1.8.2",
	}
	planPath := filepath.Join(root, "plan.json")
	if err := writeJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	if code := RunUpdater(planPath); code == 0 {
		t.Fatal("主程序没退出时不该返回成功")
	}
	if got := readFile(t, filepath.Join(root, "ClassNote.exe")); !bytes.Equal(got, oldExe) {
		t.Fatal("主程序在运行时文件被改动了")
	}
	res, ok := LastResult(root)
	if !ok || res.OK {
		t.Fatalf("应写下失败结果，实际 %+v", res)
	}
	if !strings.Contains(res.Error, "没有按时退出") {
		t.Fatalf("失败原因不对：%q", res.Error)
	}
}

// TestRunUpdaterHappyPath 覆盖「等到了退出 → 覆盖 → 写结果」，
// 重启这一步在没有 GUI 的 Linux 上必然失败，所以用退出码 4 断言。
func TestRunUpdaterHappyPath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ClassNote.exe"), fakeExe(1))
	writeFile(t, filepath.Join(root, "old-only.txt"), []byte("keep me"))
	payload := filepath.Join(t.TempDir(), "payload")
	newExe := fakeExe(2)
	writeFile(t, filepath.Join(payload, "ClassNote.exe"), newExe)
	writeFile(t, filepath.Join(payload, "web", "index.html"), []byte("new"))

	plan := Plan{
		Root: root, Payload: payload, Staging: filepath.Dir(payload),
		Backup: filepath.Join(root, "bk"),
		Log:    filepath.Join(root, "u.log"),
		Result: ResultPath(root),
		From:   "1.8.1", Version: "1.8.2",
	}
	planPath := filepath.Join(root, "plan.json")
	if err := writeJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	code := RunUpdater(planPath)
	res, ok := LastResult(root)
	if !ok {
		t.Fatal("应写下升级结果")
	}
	if !res.OK {
		t.Fatalf("升级应成功，实际 %+v", res)
	}
	if got := readFile(t, filepath.Join(root, "ClassNote.exe")); !bytes.Equal(got, newExe) {
		t.Fatal("exe 没被替换")
	}
	if got := readFile(t, filepath.Join(root, "web", "index.html")); string(got) != "new" {
		t.Fatal("静态资源没被替换")
	}
	// 重启在 Linux 上必然失败（PE 文件不可执行），退出码应为 4 而不是 0。
	if code != 4 {
		t.Fatalf("重启失败应返回 4，实际 %d", code)
	}
	if res.Restarted {
		t.Fatal("不该声称重启成功")
	}
	// 暂存目录应被清理。
	if _, err := os.Stat(plan.Staging); !os.IsNotExist(err) {
		t.Fatal("暂存目录应被清理")
	}
}

func TestSkipInPayload(t *testing.T) {
	keep := []string{"ClassNote.exe", "web/index.html", "integrations/whisper/whisper-server.exe"}
	skip := []string{"config.json", "data/sessions/s1.json", "data/reports/x.md", "run.log", "update.cmd"}
	for _, p := range keep {
		if skipInPayload(p) {
			t.Errorf("%s 不该被跳过", p)
		}
	}
	for _, p := range skip {
		if !skipInPayload(p) {
			t.Errorf("%s 应该被跳过", p)
		}
	}
}

// ---- 小工具 ----

func writeFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return b
}

// TestUpdaterKeepsUpdateDirForFlatPackage 覆盖一个把 data/updates 整目录删掉的 bug：
// build.ps1 产出的包是「平铺」的（内容直接在 zip 根上），此时内容目录就是
// staging 本身。早先清理时删的是「内容目录的父目录」，正好是 data/updates，
// 于是升级日志、last-update.json、下次的计划文件全跟着消失。
func TestUpdaterKeepsUpdateDirForFlatPackage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ClassNote.exe"), fakeExe(1))

	pkg := makeZip(t, map[string][]byte{
		"ClassNote.exe":  fakeExe(8),
		"web/index.html": []byte("new"),
	})
	payload, staging, _, err := Stage(pkg, root)
	if err != nil {
		t.Fatal(err)
	}
	if payload != staging {
		t.Fatalf("平铺包的内容目录应等于暂存目录，实际 %s / %s", payload, staging)
	}

	plan := Plan{
		Root: root, Payload: payload, Staging: staging,
		Backup: filepath.Join(Dir(root), "backup-x"),
		Log:    filepath.Join(Dir(root), "u.log"),
		Result: ResultPath(root),
		From:   "1.8.1", Version: "1.8.2",
	}
	planPath := filepath.Join(Dir(root), planName)
	if err := writeJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	code := RunUpdater(planPath)
	if code != 4 && code != 0 {
		t.Fatalf("升级应完成（重启失败返回 4），实际 %d", code)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatal("暂存目录应被清理")
	}
	// 关键断言：结果文件必须还在。
	res, ok := LastResult(root)
	if !ok || !res.OK {
		t.Fatalf("升级结果应保留在 data/updates 下，实际 %+v", res)
	}
	if _, err := os.Stat(filepath.Join(Dir(root), "u.log")); err != nil {
		t.Fatalf("升级日志不该被清理掉：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "web", "index.html")); err != nil {
		t.Fatalf("新文件应已就位：%v", err)
	}
}
