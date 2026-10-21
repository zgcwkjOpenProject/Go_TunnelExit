// webui_test 通过真实监听端口验证界面接口：服务端匿名展示、客户端配置读写、口令校验与日志接口
package test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"tunnelexit/app"
	"tunnelexit/webui"
)

// clearTunnelEnv 清空全部隧道环境变量，避免宿主机配置干扰断言
func clearTunnelEnv(t *testing.T) {
	names := []string{
		"TUNNELEXIT_APP_DIR", "TUNNELEXIT_PORT",
		"TUNNELEXIT_SOCKS_HOST", "TUNNELEXIT_SOCKS_PORT_BASE", "TUNNELEXIT_SOCKS_PORT_COUNT",
		"TUNNELEXIT_POOL_SIZE", "TUNNELEXIT_GEOIP_FILE", "TUNNELEXIT_GEOIP_FILE_V6",
		"TUNNELEXIT_SERVER", "TUNNELEXIT_CLIENT_ID", "TUNNELEXIT_CLIENT_NAME", "TUNNELEXIT_CLIENT_POOL_SIZE",
		"TUNNELEXIT_WEB_HOST", "TUNNELEXIT_WEB_PORT", "TUNNELEXIT_WEB_PATH", "TUNNELEXIT_WEB_TOKEN",
		"TUNNELEXIT_WEB_SOCKET", "TUNNELEXIT_LOG_KEEP_DAYS", "TUNNELEXIT_NODE_KEEP_DAYS",
	}
	for _, name := range names {
		t.Setenv(name, "")
	}
}

// startUI 在随机空闲端口上启动界面并返回根地址
func startUI(t *testing.T, mode webui.Mode, appDir string) string {
	t.Helper()
	return startUIWithNodes(t, mode, appDir, nil)
}

// startUIWithNodes 启动界面并注入节点状态回调，返回根地址
func startUIWithNodes(t *testing.T, mode webui.Mode, appDir string, nodes func() []app.NodeStatus) string {
	t.Helper()
	ui := startUIAt(t, mode, appDir, "", nodes)
	return "http://" + ui.Addr()
}

// startUIAt 在指定路径前缀上启动界面并注入节点状态回调
func startUIAt(t *testing.T, mode webui.Mode, appDir string, path string, nodes func() []app.NodeStatus) *webui.UI {
	t.Helper()
	ui := webui.New(webui.Options{
		Mode:   mode,
		AppDir: appDir,
		Host:   "127.0.0.1",
		Port:   0,
		Path:   path,
		Nodes:  nodes,
	})
	if err := ui.Start(); err != nil {
		t.Fatalf("启动界面失败: %v", err)
	}
	t.Cleanup(func() {
		if err := ui.Stop(); err != nil {
			t.Errorf("关闭界面失败: %v", err)
		}
	})
	return ui
}

// getBody 发起 GET 请求并返回状态码与响应体
func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, string(body)
}

// getNoRedirect 发起 GET 请求但不自动跟随跳转，额外返回 Location 头
func getNoRedirect(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, resp.Header.Get("Location")
}

// TestWebUIServesStatusPage 验证服务端根路径返回对外公开的节点页面
func TestWebUIServesStatusPage(t *testing.T) {
	clearTunnelEnv(t)
	base := startUI(t, webui.ModeServer, t.TempDir())

	status, body := getBody(t, base+"/")
	if status != http.StatusOK {
		t.Fatalf("首页状态码 %d，期望 200", status)
	}
	if !strings.Contains(body, "TunnelExit 节点状态") {
		t.Fatalf("首页缺少标题: %s", body[:200])
	}
	if strings.Contains(body, "{{") {
		t.Fatal("首页仍存在未替换的占位符")
	}
	if strings.Contains(body, "运行参数") {
		t.Fatal("公开页面不应包含配置表单")
	}
}

// TestWebUIServerExposesNoManagement 验证服务端不再暴露任何管理入口
func TestWebUIServerExposesNoManagement(t *testing.T) {
	clearTunnelEnv(t)
	base := startUI(t, webui.ModeServer, t.TempDir())

	for _, path := range []string{"/admin", "/api/config", "/api/clients", "/api/nodes/all", "/api/log"} {
		if status, _ := getBody(t, base+path); status != http.StatusNotFound {
			t.Fatalf("服务端不应提供 %s，实际状态码 %d", path, status)
		}
	}
}

// TestWebUIClientServesConfigAtRoot 验证客户端根路径就是配置管理页
func TestWebUIClientServesConfigAtRoot(t *testing.T) {
	clearTunnelEnv(t)
	base := startUI(t, webui.ModeClient, t.TempDir())

	status, body := getBody(t, base+"/")
	if status != http.StatusOK {
		t.Fatalf("客户端首页状态码 %d，期望 200", status)
	}
	if !strings.Contains(body, "TunnelExit 客户端配置") {
		t.Fatalf("客户端首页缺少标题: %s", body[:200])
	}
	if strings.Contains(body, "{{") {
		t.Fatal("客户端首页仍存在未替换的占位符")
	}
	// 客户端没有公开展示需求，也不提供匿名节点接口
	if status, _ := getBody(t, base+"/api/nodes"); status != http.StatusNotFound {
		t.Fatalf("客户端不应提供匿名节点接口，实际状态码 %d", status)
	}
}

// TestWebUIConfigDefaults 验证客户端配置接口返回默认值
func TestWebUIConfigDefaults(t *testing.T) {
	clearTunnelEnv(t)
	base := startUI(t, webui.ModeClient, t.TempDir())

	status, body := getBody(t, base+"/api/config")
	if status != http.StatusOK {
		t.Fatalf("配置接口状态码 %d，期望 200", status)
	}
	var cfg app.ClientConfig
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("解析配置失败: %v，原文 %s", err, body)
	}
	if cfg.Port != app.DefaultTunnelPort {
		t.Fatalf("隧道端口非默认值: %+v", cfg)
	}
	if cfg.WebPort != app.DefaultClientWebPort || cfg.WebHost != app.DefaultWebHost {
		t.Fatalf("界面监听非默认值: %+v", cfg)
	}
	if cfg.WebPath != app.DefaultWebPath {
		t.Fatalf("界面路径前缀非默认值: %+v", cfg)
	}
}

// TestWebUISavesClientConfig 验证界面保存后配置文件与接口同步更新
func TestWebUISavesClientConfig(t *testing.T) {
	clearTunnelEnv(t)
	appDir := t.TempDir()
	base := startUI(t, webui.ModeClient, appDir)

	payload := `{"serverHost":"10.0.0.9","port":18000,"serverKey":"-----BEGIN PUBLIC KEY-----\nY2xpZW50LXRlc3Qta2V5\n-----END PUBLIC KEY-----","id":"client-test-id","name":"nas","poolSize":8}`
	resp, err := http.Post(base+"/api/config", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存返回状态码 %d，原文 %s", resp.StatusCode, body)
	}

	status, text := getBody(t, base+"/api/config")
	if status != http.StatusOK {
		t.Fatalf("重新读取状态码 %d", status)
	}
	var cfg app.ClientConfig
	if err := json.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatalf("解析配置失败: %v，原文 %s", err, text)
	}
	if cfg.Port != 18000 || cfg.PoolSize != 8 || cfg.ServerHost != "10.0.0.9" {
		t.Fatalf("配置未生效: %+v", cfg)
	}
	if !strings.Contains(cfg.ServerKey, "BEGIN PUBLIC KEY") {
		t.Fatalf("服务端公钥未生效: %q", cfg.ServerKey)
	}
	if cfg.WebPath != app.DefaultWebPath || cfg.WebPort != app.DefaultClientWebPort {
		t.Fatalf("界面监听参数不该被页面改动: %+v", cfg)
	}
	saved := app.LoadClientConfig(filepath.Join(appDir, app.ClientConfigFile))
	if saved.ID != "client-test-id" || saved.Name != "nas" || saved.PoolSize != 8 {
		t.Fatalf("配置文件未写入预期值: %+v", saved)
	}
	if saved.ServerKey != cfg.ServerKey {
		t.Fatalf("配置文件未写入公钥: %q", saved.ServerKey)
	}
}

// TestWebUINormalizesSavedConfig 验证界面保存越界端口时自动回落到默认值
func TestWebUINormalizesSavedConfig(t *testing.T) {
	clearTunnelEnv(t)
	appDir := t.TempDir()
	base := startUI(t, webui.ModeClient, appDir)

	payload := `{"serverHost":"","port":0,"id":"","name":"","poolSize":0}`
	resp, err := http.Post(base+"/api/config", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存返回状态码 %d", resp.StatusCode)
	}

	status, text := getBody(t, base+"/api/config")
	if status != http.StatusOK {
		t.Fatalf("重新读取状态码 %d", status)
	}
	var cfg app.ClientConfig
	if err := json.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatalf("解析配置失败: %v，原文 %s", err, text)
	}
	if cfg.Port != app.DefaultTunnelPort || cfg.ServerHost != "127.0.0.1" {
		t.Fatalf("越界值未回落默认: %+v", cfg)
	}
	if cfg.WebPort != app.DefaultClientWebPort || cfg.WebHost != app.DefaultWebHost {
		t.Fatalf("界面监听参数未保持默认: %+v", cfg)
	}
	if cfg.WebPath != app.DefaultWebPath {
		t.Fatalf("界面路径前缀未保持默认: %+v", cfg)
	}
}

// TestWebUIRejectsBrokenConfig 验证格式错误的请求体不会写坏配置文件
func TestWebUIRejectsBrokenConfig(t *testing.T) {
	clearTunnelEnv(t)
	appDir := t.TempDir()
	base := startUI(t, webui.ModeClient, appDir)

	resp, err := http.Post(base+"/api/config", "application/json", strings.NewReader("{不是 JSON"))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 %d，期望 400", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(appDir, app.ClientConfigFile)); err == nil {
		t.Fatalf("格式错误时不应写出配置文件")
	}
}

// TestWebUIClientConfig 验证客户端配置接口写入隧道参数
//
// 请求体里带上界面监听参数也不会生效：改它们会让配置页本身失去访问地址
func TestWebUIClientConfig(t *testing.T) {
	clearTunnelEnv(t)
	appDir := t.TempDir()
	base := startUI(t, webui.ModeClient, appDir)

	payload := `{"serverHost":"10.0.0.9","port":19000,"id":"client-test-id","name":"nas","poolSize":4,"webHost":"0.0.0.0","webPort":17003,"webPath":"/tunnel"}`
	resp, err := http.Post(base+"/api/config", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存返回状态码 %d", resp.StatusCode)
	}

	status, text := getBody(t, base+"/api/config")
	if status != http.StatusOK {
		t.Fatalf("读取状态码 %d", status)
	}
	var cfg app.ClientConfig
	if err := json.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatalf("解析配置失败: %v，原文 %s", err, text)
	}
	if cfg.ServerHost != "10.0.0.9" || cfg.Port != 19000 || cfg.ID != "client-test-id" || cfg.Name != "nas" {
		t.Fatalf("客户端配置未生效: %+v", cfg)
	}
	if cfg.WebHost != app.DefaultWebHost || cfg.WebPort != app.DefaultClientWebPort || cfg.WebPath != app.DefaultWebPath {
		t.Fatalf("界面监听参数被请求体改动: %+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(appDir, app.ClientConfigFile)); err != nil {
		t.Fatalf("客户端配置文件未生成: %v", err)
	}
}

// TestWebUISaveKeepsCustomWebFields 验证界面保存不会覆盖磁盘上的界面监听参数
func TestWebUISaveKeepsCustomWebFields(t *testing.T) {
	clearTunnelEnv(t)
	appDir := t.TempDir()
	custom := app.DefaultClientConfig()
	custom.ServerHost = "10.0.0.1"
	custom.WebHost = "192.168.1.5"
	custom.WebPort = 18003
	custom.WebPath = "/tunnel/"
	configPath := filepath.Join(appDir, app.ClientConfigFile)
	if err := app.SaveClientConfig(configPath, custom); err != nil {
		t.Fatalf("写入初始配置失败: %v", err)
	}
	base := startUI(t, webui.ModeClient, appDir)

	payload := `{"serverHost":"10.0.0.9","port":19000,"id":"client-keep-id","name":"nas","poolSize":4,"webHost":"127.0.0.1","webPort":17003,"webPath":"/"}`
	resp, err := http.Post(base+"/api/config", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存返回状态码 %d", resp.StatusCode)
	}

	saved := app.LoadClientConfig(configPath)
	if saved.WebHost != "192.168.1.5" || saved.WebPort != 18003 || saved.WebPath != "/tunnel" {
		t.Fatalf("界面监听参数被保存覆盖: %+v", saved)
	}
	if saved.ServerHost != "10.0.0.9" || saved.Port != 19000 || saved.PoolSize != 4 {
		t.Fatalf("隧道参数未写入: %+v", saved)
	}
}

// TestWebUIServerPagesArePublic 验证服务端展示页面与匿名接口无需口令
func TestWebUIServerPagesArePublic(t *testing.T) {
	clearTunnelEnv(t)
	ui := webui.New(webui.Options{Mode: webui.ModeServer, AppDir: t.TempDir(), Host: "127.0.0.1", Port: 0, Token: "s3cret"})
	if err := ui.Start(); err != nil {
		t.Fatalf("启动界面失败: %v", err)
	}
	t.Cleanup(func() { _ = ui.Stop() })
	base := "http://" + ui.Addr()

	if status, _ := getBody(t, base+"/"); status != http.StatusOK {
		t.Fatalf("展示页面状态码 %d，期望 200", status)
	}
	if status, _ := getBody(t, base+"/api/nodes"); status != http.StatusOK {
		t.Fatalf("匿名节点接口状态码 %d，期望 200", status)
	}
}

// TestWebUIClientGuardProtectsConfig 验证客户端配置页面与接口受口令保护
func TestWebUIClientGuardProtectsConfig(t *testing.T) {
	clearTunnelEnv(t)
	ui := webui.New(webui.Options{Mode: webui.ModeClient, AppDir: t.TempDir(), Host: "127.0.0.1", Port: 0, Token: "s3cret"})
	if err := ui.Start(); err != nil {
		t.Fatalf("启动界面失败: %v", err)
	}
	t.Cleanup(func() { _ = ui.Stop() })
	base := "http://" + ui.Addr()

	if status, _ := getBody(t, base+"/"); status != http.StatusUnauthorized {
		t.Fatalf("配置页面缺少口令状态码 %d，期望 401", status)
	}
	if status, _ := getBody(t, base+"/api/config"); status != http.StatusUnauthorized {
		t.Fatalf("配置接口缺少口令状态码 %d，期望 401", status)
	}
	if status, _ := getBody(t, base+"/api/log"); status != http.StatusUnauthorized {
		t.Fatalf("日志接口缺少口令状态码 %d，期望 401", status)
	}

	req, err := http.NewRequest(http.MethodGet, base+"/api/config?token=s3cret", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("携带口令状态码 %d，期望 200", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatalf("携带口令后未下发 Cookie，后续请求仍要重复带口令")
	}
}

// TestWebUINodesAnonymous 验证公开展示接口不下发来源 IP 与客户端名称
func TestWebUINodesAnonymous(t *testing.T) {
	clearTunnelEnv(t)
	now := time.Now().Truncate(time.Second)
	nodes := []app.NodeStatus{
		{
			Address: "203.0.113.7", Name: "inner-host", Port: 18080, State: app.NodeStateOnline,
			Region: "中国 江苏省 南京市", Up: 1024, Down: 2048, ActiveStreams: 2, TotalStreams: 9, LastSeen: now,
		},
		{
			Address: "203.0.113.8", Name: "idle-host", Port: 18081, State: app.NodeStateOffline,
			Region: "未知地区",
		},
	}
	base := startUIWithNodes(t, webui.ModeServer, t.TempDir(), func() []app.NodeStatus {
		return nodes
	})

	status, body := getBody(t, base+"/api/nodes")
	if status != http.StatusOK {
		t.Fatalf("匿名节点接口状态码 %d", status)
	}
	if strings.Contains(body, "203.0.113") || strings.Contains(body, "inner-host") {
		t.Fatalf("公开接口泄露了来源 IP 或客户端名称: %s", body)
	}
	var views []struct {
		Port          int           `json:"port"`
		Region        string        `json:"region"`
		State         app.NodeState `json:"state"`
		Up            uint64        `json:"up"`
		Down          uint64        `json:"down"`
		ActiveStreams int           `json:"activeStreams"`
		TotalStreams  uint64        `json:"totalStreams"`
		LastSeen      time.Time     `json:"lastSeen"`
	}
	if err := json.Unmarshal([]byte(body), &views); err != nil {
		t.Fatalf("解析匿名节点失败: %v，原文 %s", err, body)
	}
	if len(views) != 2 {
		t.Fatalf("匿名节点条数 %d，期望 2", len(views))
	}
	if views[0].Port != 18080 || views[0].Region != "中国 江苏省 南京市" || views[0].Up != 1024 || views[0].Down != 2048 {
		t.Fatalf("匿名节点字段不符: %+v", views[0])
	}
	if views[0].State != app.NodeStateOnline || views[0].ActiveStreams != 2 || views[0].TotalStreams != 9 {
		t.Fatalf("匿名节点状态不符: %+v", views[0])
	}
	if !views[0].LastSeen.Equal(now) {
		t.Fatalf("最近在线时间未透传: %s", views[0].LastSeen)
	}
	if views[1].State != app.NodeStateOffline || !views[1].LastSeen.IsZero() {
		t.Fatalf("离线节点未透传零值时间: %+v", views[1])
	}
}

// TestWebUISubPath 验证客户端配置管理页可以挂在子路径下
func TestWebUISubPath(t *testing.T) {
	clearTunnelEnv(t)
	ui := startUIAt(t, webui.ModeClient, t.TempDir(), "/tunnel/", nil)
	base := "http://" + ui.Addr()

	status, body := getBody(t, base+"/tunnel/")
	if status != http.StatusOK {
		t.Fatalf("子路径首页状态码 %d，期望 200", status)
	}
	if !strings.Contains(body, "var BASE = '/tunnel/'") {
		t.Fatalf("页面未注入挂载路径: %s", body[:200])
	}
	if status, _ := getBody(t, base+"/"); status != http.StatusNotFound {
		t.Fatalf("根路径不应提供页面，实际状态码 %d", status)
	}
	// 路径前缀不带结尾斜杠时跳到带斜杠的地址，保留原查询参数
	status, location := getNoRedirect(t, base+"/tunnel")
	if status != http.StatusFound || location != "/tunnel/" {
		t.Fatalf("路径前缀应跳转到带斜杠地址，实际 %d/%s", status, location)
	}
	// 配置接口随页面一并挂在子路径下
	if status, _ := getBody(t, base+"/tunnel/api/config"); status != http.StatusOK {
		t.Fatalf("子路径配置接口状态码 %d，期望 200", status)
	}
	if status, _ := getBody(t, base+"/api/config"); status != http.StatusNotFound {
		t.Fatalf("根路径不应提供配置接口，实际状态码 %d", status)
	}
}

// TestWebUISubPathServerNodes 验证服务端展示页与匿名接口同样支持子路径
func TestWebUISubPathServerNodes(t *testing.T) {
	clearTunnelEnv(t)
	ui := startUIAt(t, webui.ModeServer, t.TempDir(), "/tunnel", func() []app.NodeStatus {
		return []app.NodeStatus{{Port: 18080, State: app.NodeStateOnline, Region: "中国 江苏省 南京市"}}
	})
	base := "http://" + ui.Addr()

	if status, _ := getBody(t, base+"/tunnel/"); status != http.StatusOK {
		t.Fatalf("子路径展示页状态码 %d，期望 200", status)
	}
	status, body := getBody(t, base+"/tunnel/api/nodes")
	if status != http.StatusOK {
		t.Fatalf("子路径匿名接口状态码 %d，期望 200", status)
	}
	if !strings.Contains(body, "18080") {
		t.Fatalf("子路径匿名接口未返回节点: %s", body)
	}
}

// TestWebUINodesWithoutCallback 验证没有注入回调时节点列表按空处理
func TestWebUINodesWithoutCallback(t *testing.T) {
	clearTunnelEnv(t)
	base := startUI(t, webui.ModeServer, t.TempDir())

	status, body := getBody(t, base+"/api/nodes")
	if status != http.StatusOK {
		t.Fatalf("匿名节点接口状态码 %d", status)
	}
	if strings.TrimSpace(body) != "[]" && strings.TrimSpace(body) != "null" {
		t.Fatalf("未注入回调时应返回空表，实际 %s", body)
	}
}

// TestWebUIClientModeHasNoPortMapping 验证客户端页面不含端口与流量区块
func TestWebUIClientModeHasNoPortMapping(t *testing.T) {
	clearTunnelEnv(t)
	base := startUI(t, webui.ModeClient, t.TempDir())

	status, body := getBody(t, base+"/")
	if status != http.StatusOK {
		t.Fatalf("状态码 %d", status)
	}
	if strings.Contains(body, "客户端端口与流量") || strings.Contains(body, "api/clients") {
		t.Fatal("客户端页面不应包含端口与流量区块")
	}
}

// TestWebUILogTail 验证日志接口只返回末尾指定行数
func TestWebUILogTail(t *testing.T) {
	clearTunnelEnv(t)
	appDir := t.TempDir()

	lines := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		lines = append(lines, "第"+strconv.Itoa(i)+"行")
	}
	content := strings.Join(lines, "\n") + "\n"
	// 程序日志写在运行目录的日志子目录下
	logPath := app.LogPath(appDir)
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatalf("写入日志失败: %v", err)
	}

	base := startUI(t, webui.ModeClient, appDir)
	status, body := getBody(t, base+"/api/log?lines=5")
	if status != http.StatusOK {
		t.Fatalf("日志接口状态码 %d", status)
	}
	var payload struct {
		Lines   []string `json:"lines"`
		Message string   `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析日志失败: %v，原文 %s", err, body)
	}
	if len(payload.Lines) != 5 {
		t.Fatalf("日志行数 %d，期望 5", len(payload.Lines))
	}
	if payload.Lines[4] != "第30行" {
		t.Fatalf("日志尾部不正确: %v", payload.Lines)
	}
}

// TestWebUILogMissingFile 验证日志文件不存在时不报错而是返回空列表
func TestWebUILogMissingFile(t *testing.T) {
	clearTunnelEnv(t)
	base := startUI(t, webui.ModeClient, t.TempDir())

	status, body := getBody(t, base+"/api/log")
	if status != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200", status)
	}
	var payload struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析日志失败: %v，原文 %s", err, body)
	}
	if len(payload.Lines) != 0 {
		t.Fatalf("日志文件缺失时应返回空列表，实际 %v", payload.Lines)
	}
}
