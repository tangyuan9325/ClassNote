// Package app 负责应用级元信息与窗口启动。
package app

import "runtime"

// Platform 返回平台描述。
func Platform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// Version 由构建参数注入。
var Version = "1.0.0"

// BuildTime 由构建参数注入。
var BuildTime = "dev"
