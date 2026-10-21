// api 实现 Web 界面的接口：服务端返回匿名节点列表，客户端读写配置并返回日志尾部
package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"tunnelexit/app"
	"tunnelexit/webui/assets"
)

// handleStatus 输出服务端对外公开的匿名节点展示页
func (u *UI) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != u.basePath() {
		http.NotFound(w, r)
		return
	}
	u.renderPage(w, assets.StatusPage, "TunnelExit 节点状态")
}

// handleConfigPage 输出客户端配置管理页
func (u *UI) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != u.basePath() {
		http.NotFound(w, r)
		return
	}
	u.renderPage(w, assets.ClientPage, "TunnelExit 客户端配置")
}

// renderPage 输出页面模板，替换标题、路径前缀与共用样式和函数占位符
func (u *UI) renderPage(w http.ResponseWriter, template string, title string) {
	page := renderTemplate(template, title, u.basePath())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, page)
}

// handleConfig 读取或保存客户端配置文件
func (u *UI) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, app.LoadClientConfig(u.configPath()))
	case http.MethodPost:
		u.saveConfig(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "只支持 GET 与 POST", http.StatusMethodNotAllowed)
	}
}

// configPayload 是配置页允许修改的字段集合
//
// 界面自身的监听地址、端口与路径前缀故意不在此列：它们决定了这个页面本身挂在哪里，
// 允许从页面改动就会让页面失去访问地址，只能由配置文件与 TUNNELEXIT_WEB_* 环境变量维护
type configPayload struct {
	ServerHost string `json:"serverHost"` // 隧道服务端地址
	Port       int    `json:"port"`       // 隧道服务端端口
	ServerKey  string `json:"serverKey"`  // 服务端公钥 PEM 文本，未填写时客户端拒绝连接
	ID         string `json:"id"`         // 客户端唯一标识
	Name       string `json:"name"`       // 客户端显示名
	PoolSize   int    `json:"poolSize"`   // 客户端维持的空闲连接数
}

// saveConfig 解析请求体并原子写入客户端配置文件，随后让新参数立即生效
//
// 以磁盘现有配置为底再叠加请求体，页面未提供的字段保持原值，界面监听参数因此不会被重置；
// 读取原始文件而不套用环境变量，避免把环境变量取值固化进配置文件
func (u *UI) saveConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConfigBody))
	if err != nil {
		http.Error(w, "读取请求失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	path := u.configPath()
	cfg := app.DefaultClientConfig()
	if _, err := app.ReadJSON(path, &cfg); err != nil {
		app.Logf("webui", "现有配置无法解析，按默认值重新写入: %v", err)
	}
	var payload configPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "配置格式错误: "+err.Error(), http.StatusBadRequest)
		return
	}
	cfg.ServerHost = payload.ServerHost
	cfg.Port = payload.Port
	cfg.ServerKey = payload.ServerKey
	cfg.ID = payload.ID
	cfg.Name = payload.Name
	cfg.PoolSize = payload.PoolSize
	if err := app.SaveClientConfig(path, cfg); err != nil {
		http.Error(w, "保存配置失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	app.Logf("webui", "配置已通过界面更新: %s", path)
	u.applySaved()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "配置已保存并立即生效",
	})
}

// applySaved 把保存结果交给入口回调，由入口按新参数重建隧道连接
//
// 界面自身不重启隧道也不改动监听参数，只负责通知保存结果
func (u *UI) applySaved() {
	if u.opts.OnConfigSaved == nil {
		return
	}
	u.opts.OnConfigSaved()
}

// nodeView 是公开展示接口返回的匿名节点数据
//
// 单独定义而不是复用 app.NodeStatus，让匿名化在类型层面强制生效，新增可公开字段时两处都要改
type nodeView struct {
	Port          int           `json:"port"`          // 分配给该节点的 SOCKS5 端口
	Region        string        `json:"region"`        // 来源 IP 的归属地展示文本
	State         app.NodeState `json:"state"`         // 当前状态
	Up            uint64        `json:"up"`            // 累计上行字节数
	Down          uint64        `json:"down"`          // 累计下行字节数
	ActiveStreams int           `json:"activeStreams"` // 当前正在转发的流数量
	TotalStreams  uint64        `json:"totalStreams"`  // 累计建立过的流数量
	LastSeen      time.Time     `json:"lastSeen"`      // 最近一次确认在线的时间，零值表示从未在线
}

// handleNodes 返回匿名节点列表，供公开展示页面轮询
func (u *UI) handleNodes(w http.ResponseWriter, r *http.Request) {
	nodes := u.nodes()
	views := make([]nodeView, 0, len(nodes))
	for _, node := range nodes {
		views = append(views, nodeView{
			Port:          node.Port,
			Region:        node.Region,
			State:         node.State,
			Up:            node.Up,
			Down:          node.Down,
			ActiveStreams: node.ActiveStreams,
			TotalStreams:  node.TotalStreams,
			LastSeen:      node.LastSeen,
		})
	}
	writeJSON(w, http.StatusOK, views)
}

// handleLog 返回程序日志的末尾若干行，仅客户端界面使用
func (u *UI) handleLog(w http.ResponseWriter, r *http.Request) {
	count := defaultLogLines
	if value := r.URL.Query().Get("lines"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= maxLogLines {
			count = parsed
		}
	}
	path := app.LogPath(u.opts.AppDir)
	lines, err := tailLines(path, count)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"lines": []string{}, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// tailLines 读取文件末尾的若干行，文件不存在时返回空列表
func tailLines(path string, count int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	text := strings.TrimRight(string(data), "\r\n")
	if text == "" {
		return []string{}, nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return lines, nil
}

// writeJSON 输出 JSON 响应
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
