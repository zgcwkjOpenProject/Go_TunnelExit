// api 实现 Web 界面的接口：服务端返回匿名节点列表，客户端读写配置并返回日志尾部
package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"tunnelexit/app"
	"tunnelexit/webui/assets"
)

// handlePage 输出本角色对应的页面，服务端是展示页、客户端是配置页
func (u *UI) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != u.basePath() {
		http.NotFound(w, r)
		return
	}
	template, title := assets.StatusPage, "TunnelExit 节点状态"
	if u.opts.Mode == ModeClient {
		template, title = assets.ClientPage, "TunnelExit 客户端配置"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, renderTemplate(template, title, u.basePath()))
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

// configPayload 是配置页允许修改的字段集合，界面自身的监听参数不在其中
type configPayload struct {
	ServerHost string `json:"serverHost"` // 隧道服务端地址
	Port       int    `json:"port"`       // 隧道服务端端口
	ServerKey  string `json:"serverKey"`  // 服务端公钥 PEM 文本，未填写时客户端拒绝连接
	ID         string `json:"id"`         // 客户端唯一标识
	Name       string `json:"name"`       // 客户端显示名
	PoolSize   int    `json:"poolSize"`   // 客户端维持的空闲连接数
}

// saveConfig 以磁盘现有配置为底叠加请求体后原子写入，未提供的字段保持原值
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
func (u *UI) applySaved() {
	if u.opts.OnConfigSaved == nil {
		return
	}
	u.opts.OnConfigSaved()
}

// nodeView 是公开展示接口返回的匿名节点数据，不复用 app.NodeStatus 以强制匿名化
type nodeView struct {
	Port          int           `json:"port"`          // 分配给该节点的 SOCKS5 端口
	Region        string        `json:"region"`        // 来源 IP 的归属地展示文本
	Place         string        `json:"place"`         // 地图落点键，空串表示无法定位
	State         app.NodeState `json:"state"`         // 当前状态
	Up            uint64        `json:"up"`            // 累计上行字节数
	Down          uint64        `json:"down"`          // 累计下行字节数
	UpSpeed       int64         `json:"upSpeed"`       // 最近采样区间的上行速率，单位字节每秒
	DownSpeed     int64         `json:"downSpeed"`     // 最近采样区间的下行速率，单位字节每秒
	ActiveStreams int           `json:"activeStreams"` // 当前正在转发的流数量
	TotalStreams  uint64        `json:"totalStreams"`  // 累计建立过的流数量
	LastSeen      time.Time     `json:"lastSeen"`      // 最近一次确认在线的时间，零值表示从未在线
}

// nodeViews 把节点状态裁剪成公开字段，速率由调用方按需填充
func nodeViews(nodes []app.NodeStatus) []nodeView {
	views := make([]nodeView, 0, len(nodes))
	for _, node := range nodes {
		views = append(views, nodeView{
			Port:          node.Port,
			Region:        node.Region,
			Place:         node.Place,
			State:         node.State,
			Up:            node.Up,
			Down:          node.Down,
			ActiveStreams: node.ActiveStreams,
			TotalStreams:  node.TotalStreams,
			LastSeen:      node.LastSeen,
		})
	}
	return views
}

// handleNodes 返回一次性的匿名节点列表，速率字段恒为零
func (u *UI) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, nodeViews(u.nodes()))
}

// handleStream 用 SSE 持续推送带即时速率的匿名节点列表
func (u *UI) handleStream(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		http.Error(w, "当前连接不支持实时推送", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	sampler := &speedSampler{}
	for {
		views := nodeViews(u.nodes())
		sampler.measure(views, time.Now())
		payload, err := json.Marshal(views)
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return
		}
		if err := controller.Flush(); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(streamInterval):
		}
	}
}

// speedSampler 按节点端口保存上一轮的累计字节与时刻，用于换算即时速率
type speedSampler struct {
	last map[int]speedSample
}

// speedSample 是某个节点一轮采样的累计字节数与时刻
type speedSample struct {
	up   uint64
	down uint64
	at   time.Time
}

// measure 按与上一轮的间隔填充速率字段并记录本轮采样，已消失的端口随之淘汰
func (s *speedSampler) measure(views []nodeView, now time.Time) {
	seen := make(map[int]speedSample, len(views))
	for i := range views {
		view := &views[i]
		if previous, ok := s.last[view.Port]; ok {
			seconds := now.Sub(previous.at).Seconds()
			view.UpSpeed = rate(view.Up, previous.up, seconds)
			view.DownSpeed = rate(view.Down, previous.down, seconds)
		}
		seen[view.Port] = speedSample{up: view.Up, down: view.Down, at: now}
	}
	s.last = seen
}

// rate 用两轮累计字节差除以间隔得到每秒字节数，计数回退时记为零
func rate(current uint64, previous uint64, seconds float64) int64 {
	if current <= previous || seconds <= 0 {
		return 0
	}
	return int64(float64(current-previous) / seconds)
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
