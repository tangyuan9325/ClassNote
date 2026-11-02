// Package appinfo 提供应用级元信息（版本、构建时间、平台）。
//
// 单独拆出来是为了让服务端、更新检查这类纯逻辑代码不必依赖 internal/app：
// 后者含有托盘与窗口的 Windows 实现，会把整条依赖链锁死在 Windows 上，
// 连单元测试都没法在 Linux/CI 上跑。
package appinfo

import "runtime"

// Platform 返回平台描述。
func Platform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// Version 由构建参数注入。
var Version = "1.0.0"

// BuildTime 由构建参数注入。
var BuildTime = "dev"
