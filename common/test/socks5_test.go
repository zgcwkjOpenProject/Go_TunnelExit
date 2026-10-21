// socks5_test 通过真实 TCP 连接验证服务端的 SOCKS5 握手与转发行为
package test

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"tunnelexit/socks5"
)

// echoStream 是被测代码返回的"出网连接"，直接回显收到的内容
type echoStream struct {
	conn net.Conn
}

// Read 从底层连接读取数据
func (e *echoStream) Read(p []byte) (int, error) {
	return e.conn.Read(p)
}

// Write 把数据写回底层连接，形成回显
func (e *echoStream) Write(p []byte) (int, error) {
	return e.conn.Write(p)
}

// Close 关闭底层连接
func (e *echoStream) Close() error {
	return e.conn.Close()
}

// startSocksServer 在随机端口启动一个 SOCKS5 服务，返回地址与关闭函数
func startSocksServer(t *testing.T, dial socks5.DialFunc) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = socks5.Handle(c, dial)
			}(conn)
		}
	}()
	closer := func() {
		_ = listener.Close()
		wait.Wait()
	}
	return listener.Addr().String(), closer
}

// TestSocks5ConnectAndRelay 验证 CONNECT 握手成功后数据能双向转发
func TestSocks5ConnectAndRelay(t *testing.T) {
	// 目标回显服务：收到什么都原样返回
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听目标失败: %v", err)
	}
	defer target.Close()
	go func() {
		for {
			conn, acceptErr := target.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	targetPort := target.Addr().(*net.TCPAddr).Port

	// 被测拨号函数直接连真实目标，模拟隧道打通后的出网连接
	dial := func(host string, port int) (socks5.Stream, error) {
		conn, dialErr := net.DialTimeout("tcp", socks5.FormatAddress(host, port), 3*time.Second)
		if dialErr != nil {
			return nil, dialErr
		}
		return &echoStream{conn: conn}, nil
	}
	addr, closeServer := startSocksServer(t, dial)
	defer closeServer()

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 SOCKS5 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// 方法协商：版本 5 + 一个无认证方法
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("发送协商请求失败: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("读取协商应答失败: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("协商应答异常: %v", reply)
	}

	// 请求：CONNECT 127.0.0.1:targetPort
	request := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, byte(targetPort >> 8), byte(targetPort & 0xFF)}
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}
	answer := make([]byte, 10)
	if _, err := io.ReadFull(conn, answer); err != nil {
		t.Fatalf("读取 CONNECT 应答失败: %v", err)
	}
	if answer[1] != socks5.ReplySucceeded {
		t.Fatalf("CONNECT 应答码应为 0，实际 %d", answer[1])
	}

	// 转发校验
	message := "隧道转发测试-payload"
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	echo := make([]byte, len(message))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	if string(echo) != message {
		t.Fatalf("回显不一致: 得到 %q，期望 %q", string(echo), message)
	}
}

// TestSocks5ReplyForDialError 验证拨号失败时返回映射后的应答码
func TestSocks5ReplyForDialError(t *testing.T) {
	dial := func(host string, port int) (socks5.Stream, error) {
		return nil, &socks5.ReplyError{Reply: socks5.ReplyConnectionRefused, Err: errors.New("拒绝连接")}
	}
	addr, closeServer := startSocksServer(t, dial)
	defer closeServer()

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 SOCKS5 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("发送协商请求失败: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("读取协商应答失败: %v", err)
	}
	// CONNECT 1.2.3.4:80，目标必然连不上
	request := []byte{0x05, 0x01, 0x00, 0x01, 1, 2, 3, 4, 0, 80}
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}
	answer := make([]byte, 10)
	if _, err := io.ReadFull(conn, answer); err != nil {
		t.Fatalf("读取 CONNECT 应答失败: %v", err)
	}
	if answer[1] != socks5.ReplyConnectionRefused {
		t.Fatalf("应答码应为 %d，实际 %d", socks5.ReplyConnectionRefused, answer[1])
	}
}

// TestSocks5RejectsUnsupportedCommand 验证非 CONNECT 命令被拒绝
func TestSocks5RejectsUnsupportedCommand(t *testing.T) {
	dial := func(host string, port int) (socks5.Stream, error) {
		t.Fatalf("非 CONNECT 命令不应触发拨号: %s:%d", host, port)
		return nil, nil
	}
	addr, closeServer := startSocksServer(t, dial)
	defer closeServer()

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 SOCKS5 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("发送协商请求失败: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("读取协商应答失败: %v", err)
	}
	// BIND 命令（0x02）
	request := []byte{0x05, 0x02, 0x00, 0x01, 127, 0, 0, 1, 0, 80}
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("发送 BIND 失败: %v", err)
	}
	answer := make([]byte, 10)
	if _, err := io.ReadFull(conn, answer); err != nil {
		t.Fatalf("读取应答失败: %v", err)
	}
	if answer[1] != socks5.ReplyCommandNotSupported {
		t.Fatalf("应答码应为 %d，实际 %d", socks5.ReplyCommandNotSupported, answer[1])
	}
}

// TestSocks5RejectsNoAuthNotOffered 验证客户端未提供无认证方式时被拒绝
func TestSocks5RejectsNoAuthNotOffered(t *testing.T) {
	dial := func(host string, port int) (socks5.Stream, error) {
		return nil, errors.New("不应被调用")
	}
	addr, closeServer := startSocksServer(t, dial)
	defer closeServer()

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 SOCKS5 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// 只提供用户名密码认证（0x02）
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("发送协商请求失败: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("读取协商应答失败: %v", err)
	}
	if reply[1] != 0xFF {
		t.Fatalf("应答应为 0xFF，实际 %d", reply[1])
	}
}

// TestSocks5DomainAddress 验证域名形式的地址能被正确解析
func TestSocks5DomainAddress(t *testing.T) {
	var dialedHost string
	var dialedPort int
	dial := func(host string, port int) (socks5.Stream, error) {
		dialedHost, dialedPort = host, port
		return nil, &socks5.ReplyError{Reply: socks5.ReplyHostUnreachable, Err: errors.New("域名不可达")}
	}
	addr, closeServer := startSocksServer(t, dial)
	defer closeServer()

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 SOCKS5 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("发送协商请求失败: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("读取协商应答失败: %v", err)
	}
	host := "example.com"
	request := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	request = append(request, host...)
	request = append(request, 0x01, 0xBB) // 443
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}
	answer := make([]byte, 10)
	if _, err := io.ReadFull(conn, answer); err != nil {
		t.Fatalf("读取应答失败: %v", err)
	}
	if dialedHost != host || dialedPort != 443 {
		t.Fatalf("解析出的目标错误: %s:%d", dialedHost, dialedPort)
	}
	if answer[1] != socks5.ReplyHostUnreachable {
		t.Fatalf("应答码应为 %d，实际 %d", socks5.ReplyHostUnreachable, answer[1])
	}
}

// TestReplyForErrorMapping 验证错误到应答码的映射规则
func TestReplyForErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want byte
	}{
		{name: "普通错误", err: errors.New("随便一个错误"), want: socks5.ReplyGeneralFailure},
		{name: "包装的应答错误", err: &socks5.ReplyError{Reply: socks5.ReplyTTLExpired, Err: errors.New("超时")}, want: socks5.ReplyTTLExpired},
		{name: "零值应答错误", err: &socks5.ReplyError{Err: errors.New("未分类")}, want: socks5.ReplyGeneralFailure},
		{name: "包装后仍可识别", err: wrapErr(&socks5.ReplyError{Reply: socks5.ReplyNetworkUnreachable, Err: errors.New("不可达")}), want: socks5.ReplyNetworkUnreachable},
	}
	for _, item := range cases {
		if got := socks5.ReplyForError(item.err); got != item.want {
			t.Fatalf("%s: 得到 %d，期望 %d", item.name, got, item.want)
		}
	}
}

// wrapErr 模拟上层对错误做一次包装，用于验证 errors.As 能穿透
func wrapErr(err error) error {
	return errors.Join(errors.New("上层包装"), err)
}

// TestFormatAddress 验证地址格式化对 IPv4 与域名都正确
func TestFormatAddress(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{host: "127.0.0.1", port: 80, want: "127.0.0.1:80"},
		{host: "example.com", port: 443, want: "example.com:443"},
		{host: "::1", port: 8080, want: "[::1]:8080"},
	}
	for _, item := range cases {
		if got := socks5.FormatAddress(item.host, item.port); got != item.want {
			t.Fatalf("格式化 %s:%d 得到 %q，期望 %q", item.host, item.port, got, item.want)
		}
	}
}

// TestSocks5ClosesOnClientDisconnect 验证客户端中途断开不会让服务端一直挂着
func TestSocks5ClosesOnClientDisconnect(t *testing.T) {
	dial := func(host string, port int) (socks5.Stream, error) {
		return nil, &socks5.ReplyError{Reply: socks5.ReplyConnectionRefused, Err: errors.New("拒绝")}
	}
	addr, closeServer := startSocksServer(t, dial)
	defer closeServer()

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 SOCKS5 失败: %v", err)
	}
	// 发一个残缺的协商请求后立刻断开
	if _, err := conn.Write([]byte{0x05}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	// 服务端应当已经处理完这条连接，重新连一次仍能正常握手
	again, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("二次连接失败: %v", err)
	}
	defer again.Close()
	if _, err := again.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("二次协商失败: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(again, reply); err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("二次协商应答异常: %v", reply)
	}
}

// TestSocks5ErrorText 验证应答错误的文本与解包行为
func TestSocks5ErrorText(t *testing.T) {
	inner := errors.New("底层原因")
	wrapped := &socks5.ReplyError{Reply: socks5.ReplyGeneralFailure, Err: inner}
	if !strings.Contains(wrapped.Error(), "底层原因") {
		t.Fatalf("错误文本应包含底层原因，实际 %q", wrapped.Error())
	}
	if !errors.Is(wrapped, inner) {
		t.Fatal("Unwrap 未能暴露底层错误")
	}
	empty := &socks5.ReplyError{}
	if empty.Error() == "" {
		t.Fatal("空错误也应有文本")
	}
}
