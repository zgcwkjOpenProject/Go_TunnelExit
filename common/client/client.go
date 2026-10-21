// client 实现隧道客户端：连接服务端、维持空闲连接池并按指令本地出网
package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"tunnelexit/app"
)

// 重连退避参数
const (
	minRetryInterval      = time.Second      // 重连的最短等待时间
	maxRetryInterval      = 30 * time.Second // 重连的最长等待时间
	stableSessionDuration = 60 * time.Second // 判定会话稳定的运行时长，超过后重置退避
)

// Client 是隧道客户端，负责与单个服务端保持一条控制连接
type Client struct {
	mu  sync.RWMutex     // 保护 cfg，配置可由 Web 界面在运行中更新
	cfg app.ClientConfig // 客户端运行配置
}

// New 创建隧道客户端
func New(cfg app.ClientConfig) *Client {
	return &Client{cfg: cfg}
}

// config 返回客户端当前配置的副本
//
// 运行中的会话与重连流程都会读到它，因此与写配置共用同一把读写锁
func (c *Client) config() app.ClientConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// Config 返回客户端当前配置
func (c *Client) Config() app.ClientConfig {
	return c.config()
}

// UpdateConfig 替换客户端运行配置
//
// 只写入配置变量，不会主动结束当前会话：连接参数在每次重连时才重新读取，
// 因此调用方需要自行取消正在运行的 Run 并重新启动，新参数才会真正用于拨号
func (c *Client) UpdateConfig(cfg app.ClientConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
}

// Run 连接服务端并持续运行，连接中断后按退避策略重连，直到上下文结束
func (c *Client) Run(ctx context.Context) error {
	interval := minRetryInterval
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := time.Now()
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 公钥没填或填错时重试不会成功，直接交给调用方保持进程并按固定间隔重试
		if app.IsKeyError(err) {
			return err
		}
		if err != nil && !errors.Is(err, net.ErrClosed) {
			app.Logf("client", "会话结束: %v", err)
		}
		// 稳定运行过的会话重置退避，避免偶发中断后长时间等待
		if time.Since(start) > stableSessionDuration {
			interval = minRetryInterval
		}
		app.Logf("client", "%s 后重连服务端 %s", interval, c.serverAddress())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		interval *= 2
		if interval > maxRetryInterval {
			interval = maxRetryInterval
		}
	}
}

// runOnce 建立一条登录连接并运行到结束
func (c *Client) runOnce(ctx context.Context) error {
	conn, err := c.dialServer()
	if err != nil {
		return err
	}
	current := newSession(c, conn)
	done := make(chan struct{})
	// 上下文结束时主动关闭控制连接，促使读取循环退出
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	err = current.run()
	close(done)
	return err
}

// dialServer 建立到服务端隧道端口的加密连接
//
// 服务端只接受 TLS 连接，客户端还会按配置页填入的公钥校验对端身份：
// 没填公钥或公钥与服务端不一致时直接失败，不再尝试建立明文连接
func (c *Client) dialServer() (net.Conn, error) {
	cfg := c.config()
	tlsConfig, err := app.ClientTLSConfig(cfg.ServerKey)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.NetConn().(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
	return conn, nil
}

// serverAddress 返回服务端 host:port 形式地址，便于日志展示
func (c *Client) serverAddress() string {
	cfg := c.config()
	return net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.Port))
}
