// main 是隧道客户端入口：加载配置、连接服务端并维持空闲连接池
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"tunnelexit/app"
	"tunnelexit/client"
	"tunnelexit/webui"
)

// keyRetryInterval 是公钥不可用时重新尝试连接服务端的间隔
//
// 公钥缺失或填错不会自愈，但服务端可能重新生成密钥对，因此不能一直等到用户改配置
const keyRetryInterval = time.Hour

// main 启动客户端并等待退出信号
func main() {
	appDir := app.AppDir()
	if err := os.MkdirAll(appDir, 0755); err != nil {
		app.LogFatalf("client", "创建运行目录 %s 失败: %v", appDir, err)
	}
	logPath := app.SetupLogging(app.LogPath(appDir))
	defer app.CloseLogging()
	app.Logf("client", "客户端启动，版本 %s，平台 %s，运行目录 %s",
		app.Version(), app.Platform(), appDir)

	configPath := filepath.Join(appDir, app.ClientConfigFile)
	cfg := app.LoadClientConfig(configPath)
	// 首次运行把生成的唯一 ID 落盘，保证重连沿用同一 ID 与同一 SOCKS5 端口
	cfg = app.EnsureClientID(configPath, cfg)
	app.Logf("client", "反向 SOCKS5 隧道客户端：把本机所在网络的出口交给服务端提供的 SOCKS5 端口")
	app.Logf("client", "用法：保持本进程运行，服务端日志会打印分配给本客户端的 SOCKS5 端口，把需要出网的程序代理指向 服务端地址:该端口 即用本机 IP 访问目标资源；退出请按 Ctrl+C 或发送终止信号")
	app.Logf("client", "服务端 %s:%d，客户端 ID %s，客户端名 %s，空闲连接数 %d",
		cfg.ServerHost, cfg.Port, cfg.ID, cfg.Name, cfg.PoolSize)
	// 公钥没填或填错时客户端连不上服务端，启动阶段就说清楚该去哪里改
	if _, err := app.ParsePublicKey(cfg.ServerKey); err != nil {
		app.Logf("client", "服务端公钥不可用（%v）：请打开配置管理页填入服务端 keys/public_key.pem 里的公钥，未改好前每小时自动重试一次", err)
	} else {
		app.Logf("client", "隧道连接将校验服务端公钥并全程加密")
	}
	app.Logf("client", "文件：配置 %s，日志 %s", configPath, app.LogSummary(logPath))

	instance := client.New(cfg)
	// 界面保存配置后只发一个重连信号，由主循环重载配置并重建会话，不重启进程
	reload := make(chan struct{}, 1)
	ui := webui.New(webui.Options{
		Mode:   webui.ModeClient,
		AppDir: appDir,
		Host:   cfg.WebHost,
		Port:   cfg.WebPort,
		Path:   cfg.WebPath,
		Socket: app.WebSocket(),
		Token:  app.WebToken(),
		OnConfigSaved: func() {
			select {
			case reload <- struct{}{}:
			default:
				// 已有待处理的重连信号，本次保存合并进下一次重连
			}
		},
	})
	if err := ui.Start(); err != nil {
		app.Logf("client", "配置管理页启动失败，仅影响界面: %v", err)
	}
	defer func() {
		if err := ui.Stop(); err != nil {
			app.Logf("client", "配置管理页关闭失败: %v", err)
		}
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 界面保存配置后不重启进程：取消当前会话的上下文让它主动断开，再用新参数重新连接。
	// Run 直接调用会一直阻塞到上下文结束，因此放到独立协程里，由下面的 select 决定何时收尾
	for {
		runCtx, cancel := context.WithCancel(signalCtx)
		finished := make(chan error, 1)
		go func() {
			finished <- instance.Run(runCtx)
		}()
		select {
		case <-reload:
			cancel()
			<-finished
			instance.UpdateConfig(reloadConfig(configPath))
		case err := <-finished:
			cancel()
			// 公钥没填或填错时重连不会自愈，保持进程运行并等用户在配置页改好后再重连
			if app.IsKeyError(err) {
				app.Logf("client", "无法建立加密隧道：%v", err)
				app.Logf("client", "请打开配置管理页填入服务端 keys/public_key.pem 里的公钥，保存后会自动重连；在改好之前每小时自动重试一次，本进程会保持运行")
				select {
				case <-reload:
					instance.UpdateConfig(reloadConfig(configPath))
				case <-time.After(keyRetryInterval):
				case <-signalCtx.Done():
				}
				continue
			}
			if !errors.Is(err, context.Canceled) {
				app.LogFatalf("client", "运行结束: %v", err)
			}
			app.Logf("client", "客户端已停止")
			return
		}
	}
}

// reloadConfig 重新读取磁盘配置并返回，供调用方交给客户端实例应用
//
// 保存后立即生效由这里保证：配置页只发信号，真正的重载与日志都在主循环完成
func reloadConfig(configPath string) app.ClientConfig {
	updated := app.LoadClientConfig(configPath)
	app.Logf("client", "配置已更新，按新参数重建隧道连接：服务端 %s:%d，空闲连接数 %d",
		updated.ServerHost, updated.Port, updated.PoolSize)
	return updated
}
