// Package paths 负责解析应用的各个数据目录。
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// Paths 汇总应用的各个数据目录。
type Paths struct {
	Root     string `json:"root"`
	Config   string `json:"config"`
	Sessions string `json:"sessions"`
	Captures string `json:"captures"`
	Reports  string `json:"reports"`
	Logs     string `json:"logs"`
	WebCache string `json:"webCache"`
	Bin      string `json:"bin"`
	Runtime  string `json:"runtime"`
}

// ResolvePaths 以可执行文件所在目录作为工作目录，若不可写则回落到用户目录。
func Resolve() Paths {
	exe, err := os.Executable()
	root := ""
	if err == nil {
		root = filepath.Dir(exe)
		// go run / 调试构建时 exe 位于临时目录，改用当前目录。
		if strings.Contains(strings.ToLower(root), "go-build") || strings.Contains(root, "Temp") {
			if wd, e := os.Getwd(); e == nil {
				root = wd
			}
		}
	}
	if root == "" {
		if wd, e := os.Getwd(); e == nil {
			root = wd
		}
	}
	if !writable(root) {
		if cfg, e := os.UserConfigDir(); e == nil {
			root = filepath.Join(cfg, "ClassNote")
		}
	}
	root = filepath.Clean(root)
	return Paths{
		Root:     root,
		Config:   filepath.Join(root, "config.json"),
		Sessions: filepath.Join(root, "data", "sessions"),
		Captures: filepath.Join(root, "data", "captures"),
		Reports:  filepath.Join(root, "data", "reports"),
		Logs:     filepath.Join(root, "data", "logs"),
		WebCache: filepath.Join(root, "data", "webprofile"),
		Bin:      filepath.Join(root, "data", "bin"),
		Runtime:  filepath.Join(root, "data", "runtime.json"),
	}
}

// Ensure 创建所有数据目录。
func (p Paths) Ensure() error {
	for _, d := range []string{p.Sessions, p.Captures, p.Reports, p.Logs, p.WebCache, p.Bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
