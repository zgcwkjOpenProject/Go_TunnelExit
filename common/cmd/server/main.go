// main 是隧道服务端入口：加载配置、启动 SOCKS5 端口分配与流调度
package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"tunnelexit/app"
	"tunnelexit/server"
	"tunnelexit/webui"
)

// main 启动服务端并等待退出信号
func main() {
	appDir := app.AppDir()
	if err := os.MkdirAll(appDir, 0755); err != nil {
		app.LogFatalf("server", "创建运行目录 %s 失败: %v", appDir, err)
	}
	logPath := app.SetupLogging(app.LogPath(appDir))
	defer app.CloseLogging()
	app.Logf("server", "服务端启动，版本 %s，平台 %s，运行目录 %s",
		app.Version(), app.Platform(), appDir)

	configPath := filepath.Join(appDir, app.ServerConfigFile)
	cfg := app.LoadServerConfig(configPath)
	if _, err := os.Stat(configPath); err != nil {
		if saveErr := app.SaveServerConfig(configPath, cfg); saveErr != nil {
			app.Logf("server", "写入默认配置失败: %v", saveErr)
		}
	}
	app.Logf("server", "反向 SOCKS5 隧道服务端：本机对外提供 SOCKS5 端口，出网流量由客户端所在机器完成")
	app.Logf("server", "用法：在需要使用本机出口的机器上运行 tunnelexit-client，服务端地址填本机的可达地址与隧道端口 %d，并把服务端公钥填进客户端配置页；客户端接入后本日志会打印分配到的 SOCKS5 端口，把需要出网的程序代理指向 本机地址:该端口 即可",
		cfg.Port)
	app.Logf("server", "端口：隧道端口 %d 供客户端登录并预建空闲连接，SOCKS5 端口在 %d-%d 范围内按客户端 ID 与来源 IP 分配，监听地址 %s，空闲连接数 %d",
		cfg.Port, cfg.SocksPortBase, cfg.SocksPortBase+cfg.SocksPortCount-1, cfg.SocksHost, cfg.PoolSize)
	app.Logf("server", "规则：同一客户端 ID 只接受原来源 IP 重连，旧会话仍在线时来自其他地址的同 ID 登录会被拒绝，空闲数据连接也必须与该地址一致")
	app.Logf("server", "文件：配置 %s，节点记录 %s，日志 %s",
		configPath, filepath.Join(appDir, app.ClientsStoreFile), app.LogSummary(logPath))

	// 密钥对在服务端首次启动时生成，客户端必须填写同一份公钥才能完成握手
	certificate, _, generated, err := app.LoadServerKey(appDir)
	if err != nil {
		app.LogFatalf("server", "装载服务端密钥失败: %v", err)
	}
	if generated {
		app.Logf("server", "已生成服务端密钥对，位于 %s，请妥善备份并在升级与卸载时保留",
			app.KeysDirPath(appDir))
	}
	app.Logf("server", "隧道端口只接受 TLS 连接，客户端必须填入服务端公钥才能接入；公钥文件可直接用记事本打开并整份复制：%s",
		app.ServerPublicKeyPath(appDir))

	// 走套件网关时展示页只监听套件目录内的 unix socket，不会对外暴露
	if app.WebSocket() == "" && webExposed(cfg.WebHost) {
		app.Logf("server", "提醒：节点展示页监听在 %s，该页面对外开放，只含归属地与流量统计，不包含来源 IP 与客户端名称", cfg.WebHost)
	}

	// 先建服务端再建界面，界面需要通过服务端读取节点状态
	instance := server.New(cfg, appDir, certificate)
	ui := webui.New(webui.Options{
		Mode:   webui.ModeServer,
		AppDir: appDir,
		Host:   cfg.WebHost,
		Port:   cfg.WebPort,
		Socket: app.WebSocket(),
		Path:   cfg.WebPath,
		Nodes:  instance.Nodes,
	})
	if err := ui.Start(); err != nil {
		app.Logf("server", "节点展示页启动失败，仅影响界面: %v", err)
	}
	defer func() {
		if err := ui.Stop(); err != nil {
			app.Logf("server", "节点展示页关闭失败: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := instance.Start(ctx); err != nil {
		app.LogFatalf("server", "启动失败: %v", err)
	}
	<-ctx.Done()
	app.Logf("server", "收到退出信号，正在停止")
	instance.Stop()
	app.Logf("server", "服务端已停止")
}

// webExposed 判断界面监听地址是否可能被外部访问
//
// 只有回环地址与本机名称才算安全，其余取值一律按对外暴露处理
func webExposed(host string) bool {
	value := strings.TrimSpace(host)
	if value == "" || strings.EqualFold(value, "localhost") {
		return false
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return true
	}
	return !ip.IsLoopback()
}
