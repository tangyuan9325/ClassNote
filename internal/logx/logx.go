// Package logx 提供轻量日志：写入文件并通过环形缓冲区供前端查看。
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level 日志级别。
type Level string

const (
	Debug Level = "debug"
	Info  Level = "info"
	Warn  Level = "warn"
	Error Level = "error"
)

// Entry 是一条日志。
type Entry struct {
	Time  time.Time `json:"time"`
	Level Level     `json:"level"`
	Scope string    `json:"scope"`
	Msg   string    `json:"msg"`
}

// Logger 同时写文件与内存环形缓冲。
type Logger struct {
	mu      sync.RWMutex
	file    *os.File
	buf     []Entry
	maxBuf  int
	alsoStd bool
}

var std = &Logger{maxBuf: 800}

// Setup 初始化全局日志文件。
func Setup(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := filepath.Join(dir, fmt.Sprintf("app-%s.log", time.Now().Format("20060102")))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	std.mu.Lock()
	std.file = f
	std.mu.Unlock()
	return nil
}

// EnableStdout 让日志同时输出到控制台（调试用）。
func EnableStdout() { std.mu.Lock(); std.alsoStd = true; std.mu.Unlock() }

func write(l Level, scope, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	e := Entry{Time: time.Now(), Level: l, Scope: scope, Msg: msg}

	std.mu.Lock()
	std.buf = append(std.buf, e)
	if len(std.buf) > std.maxBuf {
		std.buf = std.buf[len(std.buf)-std.maxBuf:]
	}
	if std.file != nil {
		line := fmt.Sprintf("%s [%s] %-10s %s\n", e.Time.Format("2006-01-02 15:04:05.000"), l, scope, msg)
		_, _ = std.file.WriteString(line)
	}
	if std.alsoStd {
		fmt.Print(fmt.Sprintf("%s [%s] %s\n", e.Time.Format("15:04:05"), l, scope+": "+msg))
	}
	std.mu.Unlock()
}

// Debugf 记录调试日志。
func Debugf(scope, format string, args ...any) { write(Debug, scope, format, args...) }

// Infof 记录普通日志。
func Infof(scope, format string, args ...any) { write(Info, scope, format, args...) }

// Warnf 记录警告日志。
func Warnf(scope, format string, args ...any) { write(Warn, scope, format, args...) }

// Errorf 记录错误日志。
func Errorf(scope, format string, args ...any) { write(Error, scope, format, args...) }

// Recent 返回最近 n 条日志。
func Recent(n int) []Entry {
	std.mu.RLock()
	defer std.mu.RUnlock()
	if n <= 0 || n > len(std.buf) {
		n = len(std.buf)
	}
	out := make([]Entry, n)
	copy(out, std.buf[len(std.buf)-n:])
	return out
}

// Close 关闭日志文件。
func Close() {
	std.mu.Lock()
	defer std.mu.Unlock()
	if std.file != nil {
		_ = std.file.Close()
		std.file = nil
	}
}
