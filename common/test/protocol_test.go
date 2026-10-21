// test 包对 TunnelExit 做黑盒测试，只依赖各包的公开接口
package test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"

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
