// build 保存构建期通过 ldflags 注入的版本信息
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
