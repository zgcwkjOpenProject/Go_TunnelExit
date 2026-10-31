// assets 以文件形式保存 Web 界面的前端资源，随二进制内嵌分发，运行时不读取磁盘文件
package assets

import (
	_ "embed"
	"strings"
)

// pageCSS 是两个页面共用的样式表文件
//
//go:embed page.css
var pageCSS string

// helperJS 是两个页面共用的前端函数文件
//
//go:embed helper.js
var helperJS string

// worldMapJS 是服务端展示页用的世界底图与落点文件，由 platforms/make-map.ps1 生成
//
//go:embed worldmap.js
var worldMapJS string

// clientPage 是客户端配置管理页模板文件，{{TITLE}} 与 {{BASE}} 为待替换的占位符
//
//go:embed client.html
var clientPage string

// statusPage 是服务端对外公开的匿名展示页模板文件
//
//go:embed status.html
var statusPage string

// PageStyle 是页面共用的样式表，已去掉文件首尾空行便于直接嵌在 style 标签之间
var PageStyle = strings.TrimSpace(pageCSS)

// HelperJS 是页面共用的前端函数，已去掉文件首尾空行
var HelperJS = strings.TrimSpace(helperJS)

// ClientPage 是客户端配置管理页模板，含待替换的占位符
var ClientPage = clientPage

// StatusPage 是服务端对外公开的匿名展示页模板，含待替换的占位符
var StatusPage = statusPage

// WorldMapJS 是服务端展示页的世界底图与落点，已去掉文件首尾空行
var WorldMapJS = strings.TrimSpace(worldMapJS)
