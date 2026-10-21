// e2e_test 打通"客户端 → 服务端 → 目标资源"的完整链路做端到端验证
//
// 拓扑与生产一致：
//
//	SOCKS5 客户端 → 服务端 SOCKS5 端口 → 隧道端口 → 本地客户端 → 目标服务
//
// 目标服务是真实 TCP 监听，因此测试覆盖了握手、连接池、建流、双向转发与关闭
package test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tunnelexit/app"
	"tunnelexit/client"
	"tunnelexit/server"
)

// testSocksPortCount 是测试里 SOCKS5 端口范围的长度
const testSocksPortCount = 8

// freePort 申请一个空闲端口后立刻释放，用于拿到可用的端口号
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请空闲端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

// freePortRange 找到一段连续可用端口区间的起点，避免 SOCKS5 端口范围与临时端口冲突
func freePortRange(t *testing.T, count int) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		base := freePort(t)
		if rangeAvailable(base, count) {
			return base
		}
	}
	t.Fatalf("未找到 %d 个连续可用端口", count)
	return 0
}

// freePortOutsideRange 申请一个不在指定端口区间里的空闲端口
func freePortOutsideRange(t *testing.T, base, count int) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		port := freePort(t)
		if port < base || port >= base+count {
			return port
		}
	}
	t.Fatalf("未找到范围 %d-%d 之外的可用端口", base, base+count-1)
	return 0
}

// rangeAvailable 判断一段端口是否都能立即监听
func rangeAvailable(base, count int) bool {
	listeners := make([]net.Listener, 0, count)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for port := base; port < base+count; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		if err != nil {
			return false
		}
		listeners = append(listeners, listener)
	}
	return true
}

// startEchoTarget 启动一个回显服务，作为"被访问的目标资源"
func startEchoTarget(t *testing.T) (host string, port int, stop func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动目标服务失败: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	stop = func() {
		_ = listener.Close()
		<-done
	}
	addr := listener.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, stop
}

// tunnelFixture 是一套已启动的隧道环境
type tunnelFixture struct {
	// port 是服务端隧道端口，登录与空闲数据连接共用
	port int
	// clientID 是首个客户端的唯一 ID
	clientID string
	// publicKey 是服务端公钥 PEM 文本，后续客户端要用同一份才能接入
	publicKey string
	// socksPort 是服务端分配给该客户端的 SOCKS5 端口
	socksPort int
	// instance 是服务端实例，供直接查询节点状态
	instance *server.Server
	// stop 关闭整套环境
	stop func()
}

// startTunnel 拉起服务端与客户端，客户端按 IPv4 回环接入
func startTunnel(t *testing.T, name string) *tunnelFixture {
	t.Helper()
	return startTunnelOn(t, name, "127.0.0.1")
}

// startTunnelOn 拉起服务端与客户端，connectHost 是客户端连接隧道端口使用的地址
//
// 服务端始终监听通配地址，只有客户端一侧的接入地址随参数变化，用于覆盖不同地址族
func startTunnelOn(t *testing.T, name, connectHost string) *tunnelFixture {
	t.Helper()
	root := t.TempDir()
	serverDir := filepath.Join(root, "server")
	socksBase := freePortRange(t, testSocksPortCount)
	port := freePortOutsideRange(t, socksBase, testSocksPortCount)

	serverCfg := testServerConfig(port, socksBase)
	certificate, publicKey := testKeyPair(t, serverDir)
	instance := server.New(serverCfg, serverDir, certificate)
	ctx, cancel := context.WithCancel(context.Background())
	if err := instance.Start(ctx); err != nil {
		cancel()
		t.Fatalf("启动服务端失败: %v", err)
	}

	clientID := app.NewClientID()
	node := client.New(testClientConfig(connectHost, port, clientID, name, publicKey))
	clientErr := make(chan error, 1)
	go func() { clientErr <- node.Run(ctx) }()

	// 端口由客户端 ID 与来源 IP 共同决定，登录后才确定，这里先等分配再等监听
	socksPort, err := waitForSocksPort(instance, 15*time.Second)
	if err != nil {
		cancel()
		instance.Stop()
		t.Fatalf("等待 SOCKS5 端口分配失败: %v", err)
	}
	if err := waitForPort(socksPort, 15*time.Second); err != nil {
		cancel()
		instance.Stop()
		t.Fatalf("等待 SOCKS5 端口失败: %v", err)
	}
	stop := func() {
		cancel()
		instance.Stop()
		select {
		case <-clientErr:
		case <-time.After(5 * time.Second):
		}
	}
	return &tunnelFixture{port: port, clientID: clientID, publicKey: publicKey, socksPort: socksPort, instance: instance, stop: stop}
}

// waitForSocksPort 轮询等待服务端为客户端分配 SOCKS5 端口
func waitForSocksPort(instance *server.Server, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, node := range instance.Nodes() {
			if node.Port > 0 {
				return node.Port, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("客户端在 %s 内未分配到 SOCKS5 端口", timeout)
}

// waitForNode 轮询等待指定客户端 ID 的节点出现在服务端状态里
func waitForNode(instance *server.Server, clientID string, timeout time.Duration) (app.NodeStatus, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, node := range instance.Nodes() {
			if node.ID == clientID && node.Port > 0 {
				return node, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return app.NodeStatus{}, fmt.Errorf("客户端 %s 在 %s 内未接入", clientID, timeout)
}

// waitForPort 轮询等待端口可以连接
func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("端口 %d 在 %s 内未就绪", port, timeout)
}

// TestEndToEndSocks5ThroughTunnel 验证经隧道访问真实目标服务的完整链路
func TestEndToEndSocks5ThroughTunnel(t *testing.T) {
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()

	fixture := startTunnel(t, "e2e-basic")
	defer fixture.stop()

	conn, err := dialSocks5(fixture.socksPort, targetHost, targetPort)
	if err != nil {
		t.Fatalf("SOCKS5 连接失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	payload := bytes.Repeat([]byte("tunnel-e2e-"), 200)
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatalf("回显内容不一致: 收到 %d 字节，期望 %d 字节", len(echo), len(payload))
	}
}

// TestEndToEndIPv6Client 验证客户端经 IPv6 回环接入后来源地址与转发链路都正常
func TestEndToEndIPv6Client(t *testing.T) {
	if !ipv6LoopbackAvailable() {
		t.Skip("当前环境没有 IPv6 回环地址，跳过 IPv6 接入用例")
	}
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()

	fixture := startTunnelOn(t, "e2e-ipv6", "::1")
	defer fixture.stop()

	node, err := waitForNode(fixture.instance, fixture.clientID, 15*time.Second)
	if err != nil {
		t.Fatalf("等待 IPv6 客户端接入失败: %v", err)
	}
	ip := net.ParseIP(node.Address)
	if ip == nil {
		t.Fatalf("节点来源地址 %q 不是合法 IP", node.Address)
	}
	if ip.To4() != nil {
		t.Fatalf("节点来源地址 %q 应保持 IPv6 写法", node.Address)
	}
	if !ip.IsLoopback() {
		t.Fatalf("节点来源地址 %q 不是回环地址", node.Address)
	}
	if node.Region != server.PrivateRegion {
		t.Fatalf("IPv6 回环来源应显示为%s，实际为 %q", server.PrivateRegion, node.Region)
	}

	conn, err := dialSocks5(fixture.socksPort, targetHost, targetPort)
	if err != nil {
		t.Fatalf("SOCKS5 连接失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	payload := []byte("tunnel-ipv6-client")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatalf("回显内容不一致: %q", echo)
	}
}

// ipv6LoopbackAvailable 判断当前环境是否支持 IPv6 回环监听
func ipv6LoopbackAvailable() bool {
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// TestEndToEndMultipleStreams 验证同一客户端能并发承载多条隧道流
func TestEndToEndMultipleStreams(t *testing.T) {
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()

	fixture := startTunnel(t, "e2e-multi")
	defer fixture.stop()

	const streams = 8
	errs := make(chan error, streams)
	for i := 0; i < streams; i++ {
		go func(index int) {
			conn, err := dialSocks5(fixture.socksPort, targetHost, targetPort)
			if err != nil {
				errs <- fmt.Errorf("第 %d 条流连接失败: %w", index, err)
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
			message := []byte(fmt.Sprintf("stream-%d-payload", index))
			if _, err := conn.Write(message); err != nil {
				errs <- fmt.Errorf("第 %d 条流写入失败: %w", index, err)
				return
			}
			echo := make([]byte, len(message))
			if _, err := io.ReadFull(conn, echo); err != nil {
				errs <- fmt.Errorf("第 %d 条流读取失败: %w", index, err)
				return
			}
			if !bytes.Equal(echo, message) {
				errs <- fmt.Errorf("第 %d 条流回显不一致: %q", index, echo)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < streams; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// TestEndToEndPortRefused 验证目标端口拒绝连接时返回正确的 SOCKS5 应答码
func TestEndToEndPortRefused(t *testing.T) {
	fixture := startTunnel(t, "e2e-refused")
	defer fixture.stop()

	// 该端口刚被释放，没有人监听
	deadPort := freePort(t)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(fixture.socksPort)), 5*time.Second)
	if err != nil {
		t.Fatalf("连接 SOCKS5 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	if err := socksNegotiate(conn); err != nil {
		t.Fatalf("方法协商失败: %v", err)
	}
	reply, err := socksConnect(conn, "127.0.0.1", deadPort)
	if err != nil {
		t.Fatalf("CONNECT 失败: %v", err)
	}
	if reply == 0x00 {
		t.Fatal("目标端口未监听时不应返回成功")
	}
}

// badDomainName 是不存在的域名，用于验证解析失败时的应答码
const badDomainName = "no-such-host.tunnel-e2e.invalid"

// TestEndToEndBadDomain 验证域名解析失败时返回可达性相关应答码
func TestEndToEndBadDomain(t *testing.T) {
	// 部分网络（代理或运营商劫持）会解析任意域名，此时无法构造解析失败场景
	if _, err := net.LookupHost(badDomainName); err == nil {
		t.Skipf("%s 在当前网络里能解析，跳过解析失败用例", badDomainName)
	}
	fixture := startTunnel(t, "e2e-dns")
	defer fixture.stop()

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(fixture.socksPort)), 5*time.Second)
	if err != nil {
		t.Fatalf("连接 SOCKS5 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	if err := socksNegotiate(conn); err != nil {
		t.Fatalf("方法协商失败: %v", err)
	}
	reply, err := socksConnect(conn, badDomainName, 80)
	if err != nil {
		t.Fatalf("CONNECT 失败: %v", err)
	}
	if reply == 0x00 {
		t.Fatal("域名无法解析时不应返回成功")
	}
}

// TestEndToEndClientReconnect 验证客户端退出后服务端能清理会话并允许新客户端接入
func TestEndToEndClientReconnect(t *testing.T) {
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()

	fixture := startTunnel(t, "e2e-reconnect")
	defer fixture.stop()

	first, err := dialSocks5(fixture.socksPort, targetHost, targetPort)
	if err != nil {
		t.Fatalf("首次连接失败: %v", err)
	}
	_ = first.Close()

	// 再来一轮，验证连接池与端口映射在多次连接后依然稳定
	second, err := dialSocks5(fixture.socksPort, targetHost, targetPort)
	if err != nil {
		t.Fatalf("二次连接失败: %v", err)
	}
	defer second.Close()
	_ = second.SetDeadline(time.Now().Add(15 * time.Second))
	message := []byte("reconnect-ok")
	if _, err := second.Write(message); err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(second, echo); err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	if !bytes.Equal(echo, message) {
		t.Fatalf("二次回显不一致: %q", echo)
	}
}

// dialSocks5 完成一次完整的 SOCKS5 握手并返回已连通的连接
func dialSocks5(socksPort int, host string, port int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(socksPort)), 10*time.Second)
	if err != nil {
		return nil, err
	}
	if err := socksNegotiate(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reply, err := socksConnect(conn, host, port)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if reply != 0x00 {
		_ = conn.Close()
		return nil, fmt.Errorf("CONNECT 被拒绝，应答码 %d", reply)
	}
	return conn, nil
}

// socksNegotiate 完成无认证方法协商
func socksNegotiate(conn net.Conn) error {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		return fmt.Errorf("协商应答异常: %v", reply)
	}
	return nil
}

// socksConnect 发送 CONNECT 请求并按需使用域名或 IP 地址类型
func socksConnect(conn net.Conn, host string, port int) (byte, error) {
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		request = append(request, 0x01)
		request = append(request, ip.To4()...)
	} else {
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = append(request, byte(port>>8), byte(port&0xFF))
	if _, err := conn.Write(request); err != nil {
		return 0, err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return 0, err
	}
	// 应答里带有绑定地址，长度随地址类型而定，必须读完才能继续收发
	var tail int
	switch header[3] {
	case 0x01:
		tail = 4 + 2
	case 0x04:
		tail = 16 + 2
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return 0, err
		}
		tail = int(length[0]) + 2
	default:
		return header[1], fmt.Errorf("未知地址类型 %d", header[3])
	}
	if _, err := io.ReadFull(conn, make([]byte, tail)); err != nil {
		return 0, err
	}
	return header[1], nil
}

// TestEndToEndStopIsIdempotent 验证环境可重复关闭而不 panic
func TestEndToEndStopIsIdempotent(t *testing.T) {
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()
	_ = targetHost
	_ = targetPort

	fixture := startTunnel(t, "e2e-stop")
	fixture.stop()
	fixture.stop()
}

// TestEndToEndClientReconnectsAfterServerRestart 验证服务端重启后客户端会重新登录并复用端口
//
// 重启会让客户端的控制连接与空闲连接失效，客户端必须结束旧会话并按退避间隔重连；
// 会话收尾一旦被仍在转发的隧道流阻塞，客户端就永远不会再接入
func TestEndToEndClientReconnectsAfterServerRestart(t *testing.T) {
	root := t.TempDir()
	serverDir := filepath.Join(root, "server")
	socksBase := freePortRange(t, testSocksPortCount)
	port := freePortOutsideRange(t, socksBase, testSocksPortCount)
	cfg := testServerConfig(port, socksBase)
	certificate, publicKey := testKeyPair(t, serverDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := server.New(cfg, serverDir, certificate)
	if err := first.Start(ctx); err != nil {
		t.Fatalf("启动服务端失败: %v", err)
	}
	defer first.Stop()

	clientID := app.NewClientID()
	node := client.New(testClientConfig("127.0.0.1", port, clientID, "e2e-restart", publicKey))
	clientErr := make(chan error, 1)
	go func() { clientErr <- node.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-clientErr:
		case <-time.After(5 * time.Second):
		}
	}()

	status, err := waitForNode(first, clientID, 15*time.Second)
	if err != nil {
		t.Fatalf("客户端未接入首个服务端: %v", err)
	}
	if err := waitForPort(status.Port, 15*time.Second); err != nil {
		t.Fatalf("SOCKS5 端口 %d 未就绪: %v", status.Port, err)
	}

	// 保持一条正在转发的隧道流，模拟服务端重启时客户端会话里还有活跃连接
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()
	active, err := dialSocks5(status.Port, targetHost, targetPort)
	if err != nil {
		t.Fatalf("建立隧道流失败: %v", err)
	}
	defer active.Close()
	_ = active.SetDeadline(time.Now().Add(10 * time.Second))
	message := []byte("restart-active-stream")
	if _, err := active.Write(message); err != nil {
		t.Fatalf("活跃隧道流写入失败: %v", err)
	}
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(active, echo); err != nil {
		t.Fatalf("活跃隧道流读取失败: %v", err)
	}

	first.Stop()

	// 密钥文件与节点记录一样留在运行目录，重启后公钥保持不变，客户端无需重新填写
	reloaded, reloadedKey := testKeyPair(t, serverDir)
	if reloadedKey != publicKey {
		t.Fatal("服务端重启后公钥发生了变化")
	}
	second := server.New(cfg, serverDir, reloaded)
	if err := second.Start(ctx); err != nil {
		t.Fatalf("重启服务端失败: %v", err)
	}
	defer second.Stop()

	// 端口记录会随 clients.json 一起保留，因此这里等到节点重新在线才算重连成功
	var restarted app.NodeStatus
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, item := range second.Nodes() {
			if item.ID == clientID && item.State != app.NodeStateOffline {
				restarted = item
				break
			}
		}
		if restarted.ID != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if restarted.ID == "" {
		t.Fatal("服务端重启后客户端没有重新登录")
	}
	if restarted.Port != status.Port {
		t.Fatalf("客户端重连后应复用端口 %d，实际 %d", status.Port, restarted.Port)
	}
	_ = active.Close()
}

// TestEndToEndSameClientIDTakesOver 验证同 ID 客户端从同一地址重连接管端口，隧道仍可正常出网
//
// 服务端按客户端唯一 ID 与来源 IP 分配端口，同 ID 同地址的新会话顶掉旧会话但端口不变，
// 因此这里通过端口本身仍然可用、且能完成一次回显来证明接管后链路是好的
func TestEndToEndSameClientIDTakesOver(t *testing.T) {
	fixture := startTunnel(t, "e2e-duplicate")
	defer fixture.stop()

	// 复用同一个客户端 ID 再起一个客户端，模拟客户端重启
	cfg := testClientConfig("127.0.0.1", fixture.port, fixture.clientID, "e2e-second", fixture.publicKey)
	cfg.PoolSize = 2
	node := client.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = node.Run(ctx) }()

	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()

	// 接管过程需要等第二次登录完成，这里轮询等端口重新变得可出网
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := dialSocks5(fixture.socksPort, targetHost, targetPort)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			message := []byte("duplicate-name-ok")
			if _, err = conn.Write(message); err == nil {
				echo := make([]byte, len(message))
				if _, err = io.ReadFull(conn, echo); err == nil && bytes.Equal(echo, message) {
					_ = conn.Close()
					return
				}
			}
			_ = conn.Close()
		}
		if err != nil {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("同 ID 客户端接管后隧道仍然不可用: %v", lastErr)
}

// TestEndToEndDistinctClientsGetDistinctPorts 验证同一台机器上不同 ID 的客户端各自拿到独立端口且互不影响
func TestEndToEndDistinctClientsGetDistinctPorts(t *testing.T) {
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()

	fixture := startTunnel(t, "e2e-first")
	defer fixture.stop()

	secondID := app.NewClientID()
	node := client.New(testClientConfig("127.0.0.1", fixture.port, secondID, "e2e-second-id", fixture.publicKey))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = node.Run(ctx) }()

	second, err := waitForNode(fixture.instance, secondID, 20*time.Second)
	if err != nil {
		t.Fatalf("第二个客户端未接入: %v", err)
	}
	if second.Port == fixture.socksPort {
		t.Fatalf("不同 ID 的客户端不应共用同一个 SOCKS5 端口 %d", second.Port)
	}
	if err := waitForPort(second.Port, 15*time.Second); err != nil {
		t.Fatalf("第二个客户端的 SOCKS5 端口未就绪: %v", err)
	}

	// 两个端口同时可用，说明先接入的客户端没有被顶掉
	for _, port := range []int{fixture.socksPort, second.Port} {
		conn, err := dialSocks5(port, targetHost, targetPort)
		if err != nil {
			t.Fatalf("端口 %d 出网失败: %v", port, err)
		}
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		message := []byte(fmt.Sprintf("port-%d-ok", port))
		if _, err := conn.Write(message); err != nil {
			_ = conn.Close()
			t.Fatalf("端口 %d 写入失败: %v", port, err)
		}
		echo := make([]byte, len(message))
		if _, err := io.ReadFull(conn, echo); err != nil {
			_ = conn.Close()
			t.Fatalf("端口 %d 读取失败: %v", port, err)
		}
		_ = conn.Close()
		if !bytes.Equal(echo, message) {
			t.Fatalf("端口 %d 回显不一致: %q", port, echo)
		}
	}
}

// TestEndToEndClientReloadsConfig 验证运行中替换配置并重跑客户端后按新参数接入
//
// 对应配置页保存后的处理：入口更新客户端配置、取消当前会话上下文再重新 Run，
// 客户端应断开旧服务端、沿用同一客户端 ID 登录新服务端，并在新服务端上正常出网
func TestEndToEndClientReloadsConfig(t *testing.T) {
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()

	root := t.TempDir()
	firstBase := freePortRange(t, testSocksPortCount)
	firstPort := freePortOutsideRange(t, firstBase, testSocksPortCount)
	secondBase := freePortRange(t, testSocksPortCount)
	secondPort := freePortOutsideRange(t, secondBase, testSocksPortCount)

	firstDir := filepath.Join(root, "first")
	secondDir := filepath.Join(root, "second")
	firstCertificate, firstPublicKey := testKeyPair(t, firstDir)
	secondCertificate, secondPublicKey := testKeyPair(t, secondDir)

	first := server.New(testServerConfig(firstPort, firstBase), firstDir, firstCertificate)
	second := server.New(testServerConfig(secondPort, secondBase), secondDir, secondCertificate)
	serverCtx, stopServers := context.WithCancel(context.Background())
	defer stopServers()
	if err := first.Start(serverCtx); err != nil {
		t.Fatalf("启动第一个服务端失败: %v", err)
	}
	defer first.Stop()
	if err := second.Start(serverCtx); err != nil {
		t.Fatalf("启动第二个服务端失败: %v", err)
	}
	defer second.Stop()

	clientID := app.NewClientID()
	node := client.New(testClientConfig("127.0.0.1", firstPort, clientID, "e2e-reload", firstPublicKey))

	runCtx, cancelRun := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- node.Run(runCtx) }()

	before, err := waitForNode(first, clientID, 20*time.Second)
	if err != nil {
		cancelRun()
		t.Fatalf("客户端未接入第一个服务端: %v", err)
	}
	if err := waitForPort(before.Port, 15*time.Second); err != nil {
		cancelRun()
		t.Fatalf("第一个服务端的 SOCKS5 端口未就绪: %v", err)
	}

	node.UpdateConfig(testClientConfig("127.0.0.1", secondPort, clientID, "e2e-reload", secondPublicKey))
	cancelRun()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatalf("取消上下文后客户端未结束")
	}

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	go func() { _ = node.Run(secondCtx) }()

	after, err := waitForNode(second, clientID, 20*time.Second)
	if err != nil {
		t.Fatalf("客户端未按新配置接入第二个服务端: %v", err)
	}
	if err := waitForPort(after.Port, 15*time.Second); err != nil {
		t.Fatalf("第二个服务端的 SOCKS5 端口未就绪: %v", err)
	}
	if after.Name != "e2e-reload" {
		t.Fatalf("新服务端上的客户端名不符: %+v", after)
	}

	conn, err := dialSocks5(after.Port, targetHost, targetPort)
	if err != nil {
		t.Fatalf("重连后出网失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	message := []byte("reload-ok")
	if _, err := conn.Write(message); err != nil {
		t.Fatalf("重连后写入失败: %v", err)
	}
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("重连后读取失败: %v", err)
	}
	if !bytes.Equal(echo, message) {
		t.Fatalf("重连后回显不一致: %q", echo)
	}
}

// TestEndToEndNodesReportsTraffic 验证节点状态反映实际端口、归属地与过路流量
func TestEndToEndNodesReportsTraffic(t *testing.T) {
	targetHost, targetPort, stopTarget := startEchoTarget(t)
	defer stopTarget()

	fixture := startTunnel(t, "e2e-nodes")
	defer fixture.stop()

	message := []byte("traffic-count")
	conn, err := dialSocks5(fixture.socksPort, targetHost, targetPort)
	if err != nil {
		t.Fatalf("SOCKS5 连接失败: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := conn.Write(message); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	_ = conn.Close()

	// 统计在连接关闭后才累加，这里轮询等待计数器出现
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, node := range fixture.instance.Nodes() {
			if node.Port != fixture.socksPort {
				continue
			}
			if node.Up < uint64(len(message)) || node.Down < uint64(len(message)) {
				continue
			}
			if node.Address != "127.0.0.1" {
				t.Fatalf("节点应带上来源 IP，实际 %s", node.Address)
			}
			if node.ID != fixture.clientID {
				t.Fatalf("节点应以客户端 ID 为键，实际 %s", node.ID)
			}
			if node.Name != "e2e-nodes" {
				t.Fatalf("节点名称不符: %s", node.Name)
			}
			if node.State != app.NodeStateOnline {
				t.Fatalf("节点应处于在线状态: %s", node.State)
			}
			if node.Region != server.PrivateRegion {
				t.Fatalf("本机来源地址应显示 %s，实际 %s", server.PrivateRegion, node.Region)
			}
			if node.TotalStreams == 0 {
				t.Fatal("累计连接数应在流结束后大于零")
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("节点状态未统计到过路流量")
}

// testServerConfig 构造服务端测试配置，端口全部使用动态分配值
func testServerConfig(port, socksBase int) app.ServerConfig {
	return app.ServerConfig{
		Port:           port,
		SocksHost:      "127.0.0.1",
		SocksPortBase:  socksBase,
		SocksPortCount: testSocksPortCount,
		PoolSize:       4,
	}
}

// testClientConfig 构造客户端测试配置，host 是隧道服务端地址，publicKey 为服务端公钥
func testClientConfig(host string, port int, clientID, name string, publicKey string) app.ClientConfig {
	return app.ClientConfig{
		ServerHost: host,
		Port:       port,
		ServerKey:  publicKey,
		ID:         clientID,
		Name:       name,
		PoolSize:   4,
	}
}

// testKeyPair 按服务端真实流程生成一对测试密钥并返回证书与公钥文本
func testKeyPair(t *testing.T, dir string) (tls.Certificate, string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建密钥目录失败: %v", err)
	}
	certificate, publicKey, _, err := app.LoadServerKey(dir)
	if err != nil {
		t.Fatalf("装载测试密钥失败: %v", err)
	}
	return certificate, publicKey
}
