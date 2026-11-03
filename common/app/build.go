// build 保存构建期注入的版本与平台信息
//
// 三个变量都由构建脚本通过 -ldflags -X 写入，版本号的唯一来源是 common/version
// 直接 go build 时保持占位值，启动日志照常打印
package app

// BuildVersion 是 main 包按 common/version 注入的发布版本
var BuildVersion = "0.0.0"

// BuildTime 是构建脚本按本地时间注入的构建时刻
var BuildTime = "dev"

// BuildPlatform 是构建脚本注入的打包平台标识
var BuildPlatform = "dev"

// Version 返回构建版本，未注入时给出占位值
func Version() string {
	if BuildVersion == "" {
		return "0.0.0"
	}
	return BuildVersion
}
