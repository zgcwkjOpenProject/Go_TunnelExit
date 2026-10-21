// page 把内嵌的页面模板渲染成最终 HTML，占位符由服务端替换后原样输出
//
// 前端资源全部放在 webui/assets 下的独立文件里，这里只负责替换占位符；
// 页面不引用任何外部资源，内嵌后没有外网也能打开
package webui

import (
	"strings"

	"tunnelexit/webui/assets"
)

// renderTemplate 替换页面模板里的标题、挂载路径、共用样式与共用前端函数占位符
//
// {{BASE}} 是界面挂载路径，前端据此拼接接口地址，页面挂在子路径下也能正确请求
func renderTemplate(template string, title string, base string) string {
	page := strings.ReplaceAll(template, "{{TITLE}}", title)
	page = strings.ReplaceAll(page, "{{BASE}}", base)
	page = strings.ReplaceAll(page, "{{STYLE}}", assets.PageStyle)
	page = strings.ReplaceAll(page, "{{HELPERS}}", assets.HelperJS)
	return page
}
