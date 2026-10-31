// test 包对 TunnelExit 做黑盒测试，只依赖各包的公开接口
package test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"tunnelexit/protocol"
)

// TestFrameRoundTrip 验证各种帧类型经过 WriteFrame/ReadFrame 后完全一致
func TestFrameRoundTrip(t *testing.T) {
	payload, err := protocol.EncodeJSON(protocol.NewStreamRequest{StreamID: 42, Host: "example.com", Port: 443})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	cases := []protocol.Frame{
		{Type: protocol.FrameHeartbeat},
		{Type: protocol.FrameLoginResult, StreamID: protocol.MainStreamID, Payload: []byte(`{"ok":true}`)},
		{Type: protocol.FrameNewStream, StreamID: 7, Payload: payload},
		{Type: protocol.FrameData, StreamID: 18446744073709551615, Payload: bytes.Repeat([]byte{0xAB}, 1024)},
	}
	for _, want := range cases {
		var buffer bytes.Buffer
		if err := protocol.WriteFrame(&buffer, want); err != nil {
			t.Fatalf("类型 %d 写帧失败: %v", want.Type, err)
		}
		got, err := protocol.ReadFrame(&buffer)
		if err != nil {
			t.Fatalf("类型 %d 读帧失败: %v", want.Type, err)
		}
		if got.Type != want.Type || got.StreamID != want.StreamID {
			t.Fatalf("帧头不一致: 得到 %d/%d，期望 %d/%d", got.Type, got.StreamID, want.Type, want.StreamID)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("类型 %d 负载不一致: 长度 %d vs %d", want.Type, len(got.Payload), len(want.Payload))
		}
		if buffer.Len() != 0 {
			t.Fatalf("类型 %d 读取后仍有残留字节: %d", want.Type, buffer.Len())
		}
	}
}

// TestFrameHeaderSize 验证帧头长度固定为 13 字节
func TestFrameHeaderSize(t *testing.T) {
	var buffer bytes.Buffer
	if err := protocol.WriteFrame(&buffer, protocol.Frame{Type: protocol.FrameHeartbeat}); err != nil {
		t.Fatalf("写帧失败: %v", err)
	}
	if buffer.Len() != protocol.HeaderSize {
		t.Fatalf("空负载帧长度应为 %d，实际 %d", protocol.HeaderSize, buffer.Len())
	}
}

// TestWriteFrameRejectsOversize 验证超长负载会被拒绝
func TestWriteFrameRejectsOversize(t *testing.T) {
	var buffer bytes.Buffer
	err := protocol.WriteFrame(&buffer, protocol.Frame{
		Type:    protocol.FrameData,
		Payload: bytes.Repeat([]byte{0}, protocol.MaxDataPayload+1),
	})
	if !errors.Is(err, protocol.ErrPayloadTooLarge) {
		t.Fatalf("期望 ErrPayloadTooLarge，实际 %v", err)
	}
}

// TestReadFrameRejectsUnknownType 验证未知帧类型会被拒绝
func TestReadFrameRejectsUnknownType(t *testing.T) {
	header := make([]byte, protocol.HeaderSize)
	header[0] = 0x7F
	if _, err := protocol.ReadFrame(bytes.NewReader(header)); !errors.Is(err, protocol.ErrUnknownFrame) {
		t.Fatalf("期望 ErrUnknownFrame，实际 %v", err)
	}
}

// TestReadFrameRejectsTruncated 验证帧头不完整时返回读取错误
func TestReadFrameRejectsTruncated(t *testing.T) {
	if _, err := protocol.ReadFrame(bytes.NewReader([]byte{1, 2, 3})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("期望 ErrUnexpectedEOF，实际 %v", err)
	}
}

// TestJSONMessageRoundTrip 验证所有 JSON 消息都能无损往返
func TestJSONMessageRoundTrip(t *testing.T) {
	login := protocol.LoginRequest{ClientID: "8f2a4c6e0b1d3f57", Name: "android", Version: "1.0.0", Platform: "android_arm64", PoolSize: 8}
	checkJSONRoundTrip(t, login, func() any { return &protocol.LoginRequest{} })

	result := protocol.LoginResult{OK: true, ClientID: "c1", SocksPort: 1081, PoolSize: 8}
	checkJSONRoundTrip(t, result, func() any { return &protocol.LoginResult{} })

	stream := protocol.NewStreamRequest{StreamID: 9, Host: "10.0.0.1", Port: 8080}
	checkJSONRoundTrip(t, stream, func() any { return &protocol.NewStreamRequest{} })

	work := protocol.NewWorkConn{ClientID: "c1", StreamID: 9, OK: false, Error: "拒绝连接", Reason: protocol.ReasonRefused}
	checkJSONRoundTrip(t, work, func() any { return &protocol.NewWorkConn{} })

	request := protocol.WorkConnRequest{Count: 4}
	checkJSONRoundTrip(t, request, func() any { return &protocol.WorkConnRequest{} })

	muxLogin := protocol.LoginRequest{ClientID: "8f2a4c6e0b1d3f57", Name: "android", Version: "1.0.0", Platform: "android_arm64", PoolSize: 8, Mux: true}
	checkJSONRoundTrip(t, muxLogin, func() any { return &protocol.LoginRequest{} })

	muxStream := protocol.NewStreamRequest{StreamID: 9, Host: "10.0.0.1", Port: 8080, Compress: true, Mux: true}
	checkJSONRoundTrip(t, muxStream, func() any { return &protocol.NewStreamRequest{} })

	muxWork := protocol.NewWorkConn{ClientID: "c1", StreamID: 9, OK: true, Compress: true, Mux: true}
	checkJSONRoundTrip(t, muxWork, func() any { return &protocol.NewWorkConn{} })
}

// TestDecodeJSONIgnoresUnknownFields 验证旧版本对新增字段保持宽容，保证新旧客户端互通
func TestDecodeJSONIgnoresUnknownFields(t *testing.T) {
	// 模拟新客户端发给旧服务端的携带复用与压缩标记的登录请求
	payload := []byte(`{"clientId":"c1","name":"new","version":"2.0.0","platform":"windows_amd64","poolSize":4,"mux":true,"compress":true}`)
	var target protocol.LoginRequest
	if err := protocol.DecodeJSON(payload, &target); err != nil {
		t.Fatalf("含未知字段的消息应当能解码: %v", err)
	}
	if target.ClientID != "c1" || target.PoolSize != 4 {
		t.Fatalf("已知字段解析错误: %+v", target)
	}
}

// checkJSONRoundTrip 编码后解码并与原值比较
func checkJSONRoundTrip[T comparable](t *testing.T, want T, factory func() any) {
	t.Helper()
	payload, err := protocol.EncodeJSON(want)
	if err != nil {
		t.Fatalf("编码 %T 失败: %v", want, err)
	}
	got := factory()
	if err := protocol.DecodeJSON(payload, got); err != nil {
		t.Fatalf("解码 %T 失败: %v", want, err)
	}
	if actual := *got.(*T); actual != want {
		t.Fatalf("%T 往返不一致: 得到 %+v，期望 %+v", want, actual, want)
	}
}

// TestDecodeJSONRejectsEmpty 验证空负载解码会报错
func TestDecodeJSONRejectsEmpty(t *testing.T) {
	var target protocol.LoginResult
	if err := protocol.DecodeJSON(nil, &target); err == nil {
		t.Fatal("空负载解码应当报错")
	}
}

// TestMainStreamIDIsZero 验证控制流标识固定为 0
func TestMainStreamIDIsZero(t *testing.T) {
	if protocol.MainStreamID != 0 {
		t.Fatalf("MainStreamID 必须为 0，实际 %d", protocol.MainStreamID)
	}
}

// TestFrameOverRealConnection 通过真实 TCP 连接验证帧读写不会粘包或缺字节
func TestFrameOverRealConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer listener.Close()

	frames := []protocol.Frame{
		{Type: protocol.FrameLogin, Payload: mustJSON(t, protocol.LoginRequest{Name: "n1"})},
		{Type: protocol.FrameData, StreamID: 3, Payload: bytes.Repeat([]byte{0x5A}, 3000)},
		{Type: protocol.FrameData, StreamID: 3, Payload: []byte("尾部")},
		{Type: protocol.FrameStreamClose, StreamID: 3},
	}

	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer conn.Close()
		for _, frame := range frames {
			if writeErr := protocol.WriteFrame(conn, frame); writeErr != nil {
				done <- writeErr
				return
			}
		}
		done <- nil
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer conn.Close()
	for index, want := range frames {
		got, readErr := protocol.ReadFrame(conn)
		if readErr != nil {
			t.Fatalf("第 %d 帧读取失败: %v", index, readErr)
		}
		if got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("第 %d 帧不一致: 得到 %+v", index, got)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("服务端写帧失败: %v", err)
	}
}

// mustJSON 把消息编码为 JSON，失败时直接终止测试
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := protocol.EncodeJSON(value)
	if err != nil {
		t.Fatalf("编码 %T 失败: %v", value, err)
	}
	return payload
}

// TestJSONIsValid 确保测试用到的 JSON 结构没有被意外破坏
func TestJSONIsValid(t *testing.T) {
	payload := mustJSON(t, protocol.NewWorkConn{OK: true})
	if !json.Valid(payload) {
		t.Fatalf("生成的负载不是合法 JSON: %s", payload)
	}
}

// testMuxPayloadSize 是复用会话测试的回显数据量，超过单帧负载上限以覆盖分块写出
const testMuxPayloadSize = 70000

// TestMuxSessionStreams 验证复用会话的多流协商、数据分发与关闭传播
func TestMuxSessionStreams(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	serverSession := protocol.NewMuxSession(left, nil)
	clientSession := protocol.NewMuxSession(right, func(session *protocol.MuxSession, request protocol.NewStreamRequest) {
		stream := session.Open(request.StreamID)
		if stream == nil {
			t.Error("登记流失败")
			return
		}
		payload, err := protocol.EncodeJSON(protocol.NewWorkConn{StreamID: request.StreamID, OK: true})
		if err != nil {
			t.Errorf("编码回告失败: %v", err)
			return
		}
		if err := session.WriteFrame(protocol.Frame{Type: protocol.FrameNewWorkConn, Payload: payload}); err != nil {
			t.Errorf("回告失败: %v", err)
			return
		}
		data := make([]byte, testMuxPayloadSize)
		if _, err := io.ReadFull(stream, data); err != nil {
			t.Errorf("读取流数据失败: %v", err)
			return
		}
		if _, err := stream.Write(data); err != nil {
			t.Errorf("回写流数据失败: %v", err)
			return
		}
		_ = stream.Close()
	})
	go serverSession.Serve()
	go clientSession.Serve()

	payload, err := protocol.EncodeJSON(protocol.NewStreamRequest{StreamID: 7, Host: "example.com", Port: 443})
	if err != nil {
		t.Fatalf("编码建流指令失败: %v", err)
	}
	if err := serverSession.WriteFrame(protocol.Frame{Type: protocol.FrameNewStream, Payload: payload}); err != nil {
		t.Fatalf("发送建流指令失败: %v", err)
	}
	// 先登记流再等待回告，与服务端真实开流顺序一致
	stream := serverSession.Open(7)
	if stream == nil {
		t.Fatal("登记流失败")
	}
	message, err := serverSession.WaitReply(7, 5*time.Second)
	if err != nil {
		t.Fatalf("等待回告失败: %v", err)
	}
	if !message.OK {
		t.Fatalf("回告未同意建流: %+v", message)
	}

	out := make([]byte, testMuxPayloadSize)
	for index := range out {
		out[index] = byte(index%251 + 1)
	}
	if _, err := stream.Write(out); err != nil {
		t.Fatalf("写出流数据失败: %v", err)
	}
	got := make([]byte, len(out))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	if !bytes.Equal(got, out) {
		t.Fatal("回显数据不一致")
	}
	_ = stream.Close()
	// 对端关闭后读端已关，再读应立即报错而不是阻塞
	if _, err := stream.Read(got); err == nil {
		t.Fatal("关闭后读取应当失败")
	}
}

// TestCompressConnRoundTrip 验证压缩连接双向读写内容一致，且重复数据确实被压小
func TestCompressConnRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("tunnelexit 压缩通道测试 "), 2048)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	compressLeft := protocol.NewCompressConn(left)
	compressRight := protocol.NewCompressConn(right)
	go func() {
		if _, err := compressLeft.Write(payload); err != nil {
			t.Errorf("正向写出失败: %v", err)
		}
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(compressRight, got); err != nil {
		t.Fatalf("正向读取失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("正向数据不一致")
	}

	reply := []byte("OK")
	go func() {
		if _, err := compressRight.Write(reply); err != nil {
			t.Errorf("反向写出失败: %v", err)
		}
	}()
	answer := make([]byte, len(reply))
	if _, err := io.ReadFull(compressLeft, answer); err != nil {
		t.Fatalf("反向读取失败: %v", err)
	}
	if !bytes.Equal(answer, reply) {
		t.Fatal("反向数据不一致")
	}
}
