// mux 实现在单条数据连接上承载多条隧道流的复用会话
package protocol

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// MuxSession 在一条数据连接上按流标识复用多条隧道流
//
// 建流指令 FrameNewStream 与回告 FrameNewWorkConn 沿用既有 JSON 结构，流标识以负载为准；
// 数据与流结束分别用 FrameData 与 FrameStreamClose 承载，帧头的流标识字段参与分发
type MuxSession struct {
	conn      net.Conn
	writeMu   sync.Mutex                          // 串行化连接上的帧写入
	mu        sync.Mutex                          // 保护 streams 与 pending
	streams   map[uint64]*MuxStream               // 已登记的隧道流
	pending   map[uint64]chan NewWorkConn         // 等待建流回告的流
	onStream  func(*MuxSession, NewStreamRequest) // 收到建流指令时的回调，由承接方注入
	closed    chan struct{}
	closeOnce sync.Once
}

// NewMuxSession 在已协商复用的连接上创建会话
//
// onStream 在收到建流指令时被调用，服务端开流方无需承接建流指令，传 nil 即可
func NewMuxSession(conn net.Conn, onStream func(*MuxSession, NewStreamRequest)) *MuxSession {
	return &MuxSession{
		conn:     conn,
		streams:  make(map[uint64]*MuxStream),
		pending:  make(map[uint64]chan NewWorkConn),
		onStream: onStream,
		closed:   make(chan struct{}),
	}
}

// Serve 读取连接上的帧并分发，连接结束或被关闭时返回
func (s *MuxSession) Serve() {
	defer s.closeAll()
	for {
		frame, err := ReadFrame(s.conn)
		if err != nil {
			return
		}
		switch frame.Type {
		case FrameHeartbeat:
			// 保活帧只用于维持链路
		case FrameNewStream:
			var request NewStreamRequest
			if err := DecodeJSON(frame.Payload, &request); err != nil {
				return
			}
			if s.onStream != nil {
				go s.onStream(s, request)
			}
		case FrameNewWorkConn:
			var message NewWorkConn
			if err := DecodeJSON(frame.Payload, &message); err != nil {
				return
			}
			s.deliverReply(message)
		case FrameData:
			s.deliverData(frame)
		case FrameStreamClose:
			s.closeByPeer(frame.StreamID)
		default:
			// 复用通道上出现未知帧说明对端不是预期的实现，直接结束
			return
		}
	}
}

// WriteFrame 在连接上串行写出一帧
func (s *MuxSession) WriteFrame(frame Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return WriteFrame(s.conn, frame)
}

// Open 登记一条流并返回其读写端，流标识重复时返回 nil
func (s *MuxSession) Open(streamID uint64) *MuxStream {
	reader, writer := io.Pipe()
	stream := &MuxStream{session: s, id: streamID, reader: reader, writer: writer}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.streams[streamID]; exists {
		return nil
	}
	s.streams[streamID] = stream
	return stream
}

// WaitReply 等待指定流标识的建流回告
func (s *MuxSession) WaitReply(streamID uint64, timeout time.Duration) (NewWorkConn, error) {
	waiter := make(chan NewWorkConn, 1)
	s.mu.Lock()
	s.pending[streamID] = waiter
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, streamID)
		s.mu.Unlock()
	}()
	select {
	case message := <-waiter:
		return message, nil
	case <-time.After(timeout):
		return NewWorkConn{}, errors.New("等待建流回告超时")
	case <-s.closed:
		return NewWorkConn{}, errors.New("复用连接已关闭")
	}
}

// Discard 撤销一条尚未交付的流登记，不向对端发任何帧
func (s *MuxSession) Discard(streamID uint64) {
	s.mu.Lock()
	stream := s.streams[streamID]
	delete(s.streams, streamID)
	s.mu.Unlock()
	if stream != nil {
		stream.discard()
	}
}

// Close 关闭底层连接，Serve 随之返回
func (s *MuxSession) Close() error {
	return s.conn.Close()
}

// deliverReply 把建流回告交给等待方，没有等待方时忽略
func (s *MuxSession) deliverReply(message NewWorkConn) {
	s.mu.Lock()
	waiter, ok := s.pending[message.StreamID]
	delete(s.pending, message.StreamID)
	s.mu.Unlock()
	if ok {
		waiter <- message
	}
}

// deliverData 把数据帧写入对应流，流已不存在时回告关闭让对端尽快失败
func (s *MuxSession) deliverData(frame Frame) {
	s.mu.Lock()
	stream := s.streams[frame.StreamID]
	s.mu.Unlock()
	if stream == nil {
		_ = s.WriteFrame(Frame{Type: FrameStreamClose, StreamID: frame.StreamID})
		return
	}
	// 写入流内管道即完成背压传递，消费方读走之前不会继续读连接
	_, _ = stream.write(frame.Payload)
}

// closeByPeer 处理对端的流关闭，读方向得到 EOF，写方向报错
func (s *MuxSession) closeByPeer(streamID uint64) {
	s.mu.Lock()
	stream := s.streams[streamID]
	delete(s.streams, streamID)
	s.mu.Unlock()
	if stream != nil {
		stream.peerClosed()
	}
}

// removeStream 撤销流登记
func (s *MuxSession) removeStream(streamID uint64) {
	s.mu.Lock()
	delete(s.streams, streamID)
	s.mu.Unlock()
}

// closeAll 结束会话并让所有流的读写端解除阻塞
func (s *MuxSession) closeAll() {
	s.closeOnce.Do(func() {
		close(s.closed)
	})
	s.mu.Lock()
	streams := s.streams
	s.streams = make(map[uint64]*MuxStream)
	s.mu.Unlock()
	for _, stream := range streams {
		stream.peerClosed()
	}
}

// MuxStream 是复用连接上的一条隧道流，读写行为等价于独立连接
type MuxStream struct {
	session   *MuxSession
	id        uint64
	reader    *io.PipeReader
	writer    *io.PipeWriter
	closeOnce sync.Once
}

// Read 读取对端写来的流数据
func (m *MuxStream) Read(p []byte) (int, error) {
	return m.reader.Read(p)
}

// Write 把数据按帧负载上限分块写出
func (m *MuxStream) Write(p []byte) (int, error) {
	for written := 0; written < len(p); {
		size := len(p) - written
		if size > MaxDataPayload {
			size = MaxDataPayload
		}
		if err := m.session.WriteFrame(Frame{Type: FrameData, StreamID: m.id, Payload: p[written : written+size]}); err != nil {
			return written, err
		}
		written += size
	}
	return len(p), nil
}

// Close 关闭流并通知对端
func (m *MuxStream) Close() error {
	m.closeOnce.Do(func() {
		m.session.removeStream(m.id)
		_ = m.reader.Close()
		_ = m.writer.Close()
		_ = m.session.WriteFrame(Frame{Type: FrameStreamClose, StreamID: m.id})
	})
	return nil
}

// write 供分发协程写入对端数据
func (m *MuxStream) write(p []byte) (int, error) {
	return m.writer.Write(p)
}

// peerClosed 由对端关闭或会话结束时调用，读方向得到 EOF，写方向报错
func (m *MuxStream) peerClosed() {
	m.closeOnce.Do(func() {
		_ = m.writer.CloseWithError(nil)
		_ = m.reader.Close()
	})
}

// discard 撤销登记后静默关闭两端
func (m *MuxStream) discard() {
	m.closeOnce.Do(func() {
		_ = m.reader.Close()
		_ = m.writer.Close()
	})
}
