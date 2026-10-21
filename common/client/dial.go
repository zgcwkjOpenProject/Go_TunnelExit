// dial 负责客户端本地出网拨号，并把失败原因归类上报给服务端
package client

import (
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"

	"tunnelexit/protocol"
)

// dialTarget 在本地建立到目标地址的连接
//
// 返回的 reason 用于让服务端选择对应的 SOCKS5 应答码
func dialTarget(host string, port int, timeout time.Duration) (net.Conn, string, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, classifyDialError(err), err
	}
	return conn, "", nil
}

// classifyDialError 把拨号错误归类为协议约定的失败原因
func classifyDialError(err error) string {
	if err == nil {
		return ""
	}
	// 域名解析失败归为地址不可达，SOCKS5 客户端可据此换用其他出口
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return protocol.ReasonUnreachable
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return protocol.ReasonRefused
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return protocol.ReasonUnreachable
	}
	if errors.Is(err, syscall.ETIMEDOUT) {
		return protocol.ReasonTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return protocol.ReasonTimeout
	}
	return protocol.ReasonOther
}
