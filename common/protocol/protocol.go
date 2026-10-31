// protocol 定义隧道两端共用的帧格式与消息结构
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// FrameType 标识一个隧道帧的用途
type FrameType uint8

// 隧道帧类型，控制帧承载 JSON，数据帧承载原始字节
const (
	FrameHeartbeat       FrameType = 1 // 控制连接上的心跳
	FrameHeartbeatAck    FrameType = 2 // 心跳应答
	FrameLogin           FrameType = 3 // 客户端登录请求，payload 为 LoginRequest
	FrameLoginResult     FrameType = 4 // 服务端登录应答，payload 为 LoginResult
	FrameNewStream       FrameType = 5 // 服务端下发的新连接请求，payload 为 NewStreamRequest
	FrameNewWorkConn     FrameType = 6 // 客户端在隧道连接首帧上报的归属信息与建流回告，payload 为 NewWorkConn
	FrameStreamClose     FrameType = 7 // 复用通道上一条隧道流结束，流标识在帧头
	FrameData            FrameType = 8 // 复用通道上隧道流的原始字节，流标识在帧头
	FrameWorkConnRequest FrameType = 9 // 服务端要求客户端补充空闲连接，payload 为 WorkConnRequest
)

// 拨号失败原因，客户端据此上报，服务端映射为 SOCKS5 应答码
const (
	ReasonRefused     = "refused"     // 目标端口拒绝连接
	ReasonTimeout     = "timeout"     // 连接目标超时
	ReasonUnreachable = "unreachable" // 目标地址不可达或域名解析失败
	ReasonOther       = "other"       // 其他本地错误
)

// 帧头长度：1 字节类型 + 8 字节流标识 + 4 字节负载长度
const HeaderSize = 13

// 单帧负载上限，控制帧为 JSON，数据帧为读写缓冲大小
const (
	MaxJSONPayload = 64 * 1024 // 控制帧 JSON 负载
	MaxDataPayload = 32 * 1024 // 数据帧读写缓冲
)

// 协议层错误
var (
	ErrPayloadTooLarge = errors.New("帧负载超过上限") // 对端声明的负载长度超过允许上限
	ErrUnknownFrame    = errors.New("未知的帧类型")  // 收到未定义类型的帧
)

// MainStreamID 是控制流保留标识，空闲数据连接首帧以它上报归属
const MainStreamID uint64 = 0

// Frame 是一条已解析的隧道帧
type Frame struct {
	Type     FrameType // 帧用途
	StreamID uint64    // 标识同一条隧道流，0 表示控制流
	Payload  []byte    // 帧负载，数据帧为原始字节
}

// WriteFrame 按固定帧头格式写出一帧
func WriteFrame(writer io.Writer, frame Frame) error {
	length := len(frame.Payload)
	limit := MaxJSONPayload
	// 数据帧载荷比 JSON 帧小，保留类型时按数据帧上限校验
	if frame.Type == FrameData {
		limit = MaxDataPayload
	}
	if length > limit {
		return ErrPayloadTooLarge
	}
	header := make([]byte, HeaderSize)
	header[0] = byte(frame.Type)
	binary.BigEndian.PutUint64(header[1:9], frame.StreamID)
	binary.BigEndian.PutUint32(header[9:13], uint32(length))
	if _, err := writer.Write(header); err != nil {
		return err
	}
	if length == 0 {
		return nil
	}
	_, err := writer.Write(frame.Payload)
	return err
}

// ReadFrame 读取并解析一帧，EOF 时原样返回
func ReadFrame(reader io.Reader) (Frame, error) {
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return Frame{}, err
	}
	kind := FrameType(header[0])
	var limit int
	switch kind {
	case FrameHeartbeat, FrameHeartbeatAck, FrameStreamClose, FrameLogin, FrameLoginResult, FrameNewStream, FrameNewWorkConn, FrameWorkConnRequest:
		limit = MaxJSONPayload
	case FrameData:
		limit = MaxDataPayload
	default:
		return Frame{}, fmt.Errorf("%w: %d", ErrUnknownFrame, header[0])
	}
	length := int(binary.BigEndian.Uint32(header[9:13]))
	if length > limit {
		return Frame{}, ErrPayloadTooLarge
	}
	frame := Frame{
		Type:     kind,
		StreamID: binary.BigEndian.Uint64(header[1:9]),
	}
	if length > 0 {
		frame.Payload = make([]byte, length)
		if _, err := io.ReadFull(reader, frame.Payload); err != nil {
			return Frame{}, err
		}
	}
	return frame, nil
}

// LoginRequest 是客户端登录时上报的自身信息
type LoginRequest struct {
	ClientID string `json:"clientId"`      // 客户端唯一标识，服务端结合来源 IP 为它固定分配 SOCKS5 端口
	Name     string `json:"name"`          // 客户端显示名，只用于服务端日志与节点记录
	Version  string `json:"version"`       // 客户端版本，仅用于日志与节点记录
	Platform string `json:"platform"`      // 客户端打包平台标识
	PoolSize int    `json:"poolSize"`      // 客户端希望维持的空闲连接数
	Mux      bool   `json:"mux,omitempty"` // 客户端是否支持多路复用，支持时服务端只维持少量载体连接
}

// LoginResult 是服务端返回的登录结果
type LoginResult struct {
	OK        bool   `json:"ok"`                  // 登录是否成功
	Error     string `json:"error,omitempty"`     // 失败原因
	ClientID  string `json:"clientId,omitempty"`  // 登录时确认的客户端标识
	SocksPort int    `json:"socksPort,omitempty"` // 分配给该客户端的 SOCKS5 端口
	PoolSize  int    `json:"poolSize,omitempty"`  // 服务端最终确定的连接池大小
}

// NewStreamRequest 是服务端要求客户端建立一条出网连接的指令
type NewStreamRequest struct {
	StreamID uint64 `json:"streamId"`           // 服务端分配的流标识
	Host     string `json:"host"`               // SOCKS5 请求的目标地址
	Port     int    `json:"port"`               // SOCKS5 请求的目标端口
	Compress bool   `json:"compress,omitempty"` // 服务端是否请求对该流启用压缩，客户端同意时回告置位
	Mux      bool   `json:"mux,omitempty"`      // 服务端是否请求在该连接上多路复用，同意时回告必须置位
}

// NewWorkConn 是客户端在隧道连接首帧上报的归属信息
type NewWorkConn struct {
	ClientID string `json:"clientId"`           // 客户端唯一标识，服务端据此找到会话
	StreamID uint64 `json:"streamId"`           // 目标流标识，控制流标识表示该连接加入空闲池
	OK       bool   `json:"ok"`                 // 本地出网连接是否建立成功
	Error    string `json:"error,omitempty"`    // 建立失败时的原因，仅用于日志
	Reason   string `json:"reason,omitempty"`   // 建立失败的原因分类，服务端据此选择 SOCKS5 应答码
	Compress bool   `json:"compress,omitempty"` // 回告是否同意压缩，旧对端不认识该字段时保持未设置
	Mux      bool   `json:"mux,omitempty"`      // 回告是否同意多路复用，同意后该连接转为复用载体
}

// WorkConnRequest 是服务端要求客户端补充空闲连接的指令
type WorkConnRequest struct {
	Count int `json:"count"` // 需要补充的连接数量
}

// EncodeJSON 把消息序列化为 JSON 负载
func EncodeJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

// DecodeJSON 把帧负载反序列化为消息
func DecodeJSON(payload []byte, value any) error {
	if len(payload) == 0 {
		return errors.New("帧负载为空")
	}
	return json.Unmarshal(payload, value)
}
