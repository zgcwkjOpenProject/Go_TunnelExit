// socks5 实现无认证的 SOCKS5 CONNECT 服务，实际出网交给注入的拨号函数
package socks5

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// 协议版本与命令常量
const (
	version5 = 0x05

	methodNoAuth       = 0x00
	methodNoAcceptable = 0xFF

	commandConnect = 0x01

	addressIPv4   = 0x01
	addressDomain = 0x03
	addressIPv6   = 0x04

	// HandshakeTimeout 是握手阶段允许的最长耗时，防止半开连接占用端口
	HandshakeTimeout = 30 * time.Second
)

// 应答码，用于回告 SOCKS5 客户端失败原因
const (
	ReplySucceeded           = 0x00
	ReplyGeneralFailure      = 0x01
	ReplyNotAllowed          = 0x02
	ReplyNetworkUnreachable  = 0x03
	ReplyHostUnreachable     = 0x04
	ReplyConnectionRefused   = 0x05
	ReplyTTLExpired          = 0x06
	ReplyCommandNotSupported = 0x07
	ReplyAddressNotSupported = 0x08
)

// 协议层错误
var (
	// ErrNoAcceptableMethod 表示客户端未提供无认证方式
	ErrNoAcceptableMethod = errors.New("客户端不支持无认证方式")
	// ErrUnsupportedCommand 表示客户端请求了 CONNECT 以外的命令
	ErrUnsupportedCommand = errors.New("仅支持 CONNECT 命令")
	// ErrUnsupportedAddress 表示客户端使用了未支持的地址类型
	ErrUnsupportedAddress = errors.New("不支持的地址类型")
)

// Stream 是隧道建立后的出网连接，读写行为等价于一条 TCP 连接
type Stream interface {
	io.ReadWriteCloser
}

// DialFunc 按目标地址建立隧道流，失败时返回错误
type DialFunc func(host string, port int) (Stream, error)

// ReplyError 把拨号失败原因映射为 SOCKS5 应答码
type ReplyError struct {
	// Reply 是返回给客户端的应答码
	Reply byte
	// Err 是原始错误，用于服务端日志
	Err error
}

// Error 返回原始错误描述
func (e *ReplyError) Error() string {
	if e.Err == nil {
		return "socks5 应答失败"
	}
	return e.Err.Error()
}

// Unwrap 暴露原始错误，便于 errors.Is 判断
func (e *ReplyError) Unwrap() error {
	return e.Err
}

// ReplyForError 把任意拨号错误转换成 SOCKS5 应答码，默认为常规失败
func ReplyForError(err error) byte {
	var replyErr *ReplyError
	if errors.As(err, &replyErr) && replyErr.Reply != 0 {
		return replyErr.Reply
	}
	return ReplyGeneralFailure
}

// Handle 处理一条 SOCKS5 连接，完成握手、拨号并双向转发数据
//
// 返回的错误仅用于服务端日志，调用方应在连接结束后关闭 conn
func Handle(conn net.Conn, dial DialFunc) error {
	deadline := time.Now().Add(HandshakeTimeout)
	_ = conn.SetDeadline(deadline)
	target, err := handshake(conn)
	if err != nil {
		return err
	}
	stream, err := dial(target.host, target.port)
	if err != nil {
		replyErr := ReplyForError(err)
		_ = writeReply(conn, replyErr)
		return err
	}
	// 握手结束后取消超时，转发阶段由两端各自的读写错误结束
	_ = conn.SetDeadline(time.Time{})
	if err := writeReply(conn, ReplySucceeded); err != nil {
		_ = stream.Close()
		return err
	}
	relay(conn, stream)
	return nil
}

// target 是 SOCKS5 请求解析出的目标地址
type target struct {
	host string // 目标主机，可能是域名或地址字面量
	port int    // 目标端口
}

// handshake 完成方法协商与请求解析，返回目标地址
func handshake(conn net.Conn) (target, error) {
	if err := negotiateMethod(conn); err != nil {
		return target{}, err
	}
	return readRequest(conn)
}

// negotiateMethod 完成无认证方法协商
func negotiateMethod(conn net.Conn) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return fmt.Errorf("读取方法协商头失败: %w", err)
	}
	if header[0] != version5 {
		return fmt.Errorf("不支持的 SOCKS 版本: %d", header[0])
	}
	count := int(header[1])
	if count == 0 {
		return ErrNoAcceptableMethod
	}
	methods := make([]byte, count)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return fmt.Errorf("读取方法列表失败: %w", err)
	}
	acceptable := false
	for _, method := range methods {
		if method == methodNoAuth {
			acceptable = true
			break
		}
	}
	if !acceptable {
		_ = writeAll(conn, []byte{version5, methodNoAcceptable})
		return ErrNoAcceptableMethod
	}
	return writeAll(conn, []byte{version5, methodNoAuth})
}

// readRequest 解析 CONNECT 请求，仅支持 CONNECT 命令
func readRequest(conn net.Conn) (target, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return target{}, fmt.Errorf("读取请求头失败: %w", err)
	}
	if header[0] != version5 {
		return target{}, fmt.Errorf("不支持的 SOCKS 版本: %d", header[0])
	}
	if header[1] != commandConnect {
		_ = writeReply(conn, ReplyCommandNotSupported)
		return target{}, ErrUnsupportedCommand
	}
	host, err := readAddress(conn, header[3])
	if err != nil {
		_ = writeReply(conn, ReplyAddressNotSupported)
		return target{}, err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return target{}, fmt.Errorf("读取目标端口失败: %w", err)
	}
	return target{host: host, port: int(binary.BigEndian.Uint16(portBytes))}, nil
}

// readAddress 按地址类型读取目标地址
func readAddress(conn net.Conn, addressType byte) (string, error) {
	switch addressType {
	case addressIPv4:
		buffer := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, buffer); err != nil {
			return "", fmt.Errorf("读取 IPv4 地址失败: %w", err)
		}
		return net.IP(buffer).String(), nil
	case addressIPv6:
		buffer := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, buffer); err != nil {
			return "", fmt.Errorf("读取 IPv6 地址失败: %w", err)
		}
		return net.IP(buffer).String(), nil
	case addressDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", fmt.Errorf("读取域名长度失败: %w", err)
		}
		if length[0] == 0 {
			return "", errors.New("域名为空")
		}
		buffer := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, buffer); err != nil {
			return "", fmt.Errorf("读取域名失败: %w", err)
		}
		return string(buffer), nil
	default:
		return "", fmt.Errorf("%w: %d", ErrUnsupportedAddress, addressType)
	}
}

// writeReply 返回应答，绑定地址统一填 0.0.0.0:0 表示由隧道侧确定
func writeReply(conn net.Conn, reply byte) error {
	return writeAll(conn, []byte{version5, reply, 0x00, addressIPv4, 0, 0, 0, 0, 0, 0})
}

// writeAll 写完整段数据
func writeAll(conn net.Conn, data []byte) error {
	_, err := conn.Write(data)
	return err
}

// relay 在两个连接之间双向转发数据，任意一侧关闭后同时关闭另一侧
func relay(left net.Conn, right Stream) {
	done := make(chan struct{}, 2)
	copyStream := func(dst io.Writer, src io.Reader) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go copyStream(left, right)
	go copyStream(right, left)
	<-done
	_ = left.Close()
	_ = right.Close()
	// 等待另一方向退出，避免协程在连接释放后继续写入
	<-done
}

// FormatAddress 把主机和端口拼成 host:port 形式
func FormatAddress(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
