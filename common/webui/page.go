// page 把内嵌的页面模板渲染成最终 HTML，占位符由服务端替换后原样输出，页面不引用任何外部资源
package webui

import (
	"strings"

	"tunnelexit/webui/assets"
)

// renderTemplate 替换页面模板里的标题、挂载路径、共用样式、共用前端函数与地图占位符，前端据 {{BASE}} 拼接口地址
func renderTemplate(template string, title string, base string) string {
	page := strings.ReplaceAll(template, "{{TITLE}}", title)
	page = strings.ReplaceAll(page, "{{BASE}}", base)
	page = strings.ReplaceAll(page, "{{STYLE}}", assets.PageStyle)
	page = strings.ReplaceAll(page, "{{HELPERS}}", assets.HelperJS)
	page = strings.ReplaceAll(page, "{{MAP}}", assets.WorldMapJS)
	return page
}
