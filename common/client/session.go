// session 实现客户端单次连接的生命周期：登录、空闲连接池与隧道流转发
package client

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"tunnelexit/app"
	"tunnelexit/protocol"
)

// 客户端会话参数
const (
	dialTimeout       = 15 * time.Second // 连接服务端与目标地址的超时
	handshakeTimeout  = 15 * time.Second // 登录、数据连接上报与结果回告的超时
	loginTimeout      = 20 * time.Second // 等待登录结果的超时，需覆盖服务端注册耗时
	heartbeatInterval = 30 * time.Second // 向服务端发送心跳的间隔
	heartbeatTimeout  = 90 * time.Second // 连续未收到服务端心跳后判定连接失效的时长
	bufferSize        = 32 * 1024        // 隧道流双向转发的缓冲大小
	fillRetryInterval = time.Second      // 补池失败后的重试间隔
)

// session 表示客户端与服务端之间的一次会话
//
// 空闲连接池由服务端单向驱动：服务端按需下发补池请求，客户端建立对应数量的数据连接推送过去
type session struct {
	client       *Client        // 所属客户端
	conn         net.Conn       // 登录连接
	clientID     string         // 客户端唯一标识，登录时上报，服务端据此分配端口并识别空闲连接
	socksPort    int            // 服务端分配的 SOCKS5 端口
	poolSize     int            // 与服务端约定的空闲连接数
	writeMu      sync.Mutex     // 串行化控制连接上的帧写入
	mu           sync.Mutex     // 保护 idleConns 与 busyConns
	idleConns    []net.Conn     // 已建立、等待服务端下发建流指令的数据连接
	busyConns    []net.Conn     // 已取出、正在转发的隧道连接，会话结束时必须与空闲连接一起关闭
	fillReqs     chan int       // 承载服务端下发的补池请求数量
	lastPongNano atomic.Int64   // 最近一次服务端心跳时间
	closeOnce    sync.Once      // 保证会话只关闭一次
	closed       chan struct{}  // 在会话结束时关闭
	wg           sync.WaitGroup // 等待所有后台协程退出
}

// newSession 创建客户端会话
func newSession(client *Client, conn net.Conn) *session {
	s := &session{
		client:   client,
		conn:     conn,
		clientID: client.config().ID,
		fillReqs: make(chan int, 64),
		closed:   make(chan struct{}),
	}
	s.lastPongNano.Store(time.Now().UnixNano())
	return s
}

// run 执行一次完整会话，返回时保证所有连接已释放
func (s *session) run() error {
	defer s.close()
	if err := s.login(); err != nil {
		return err
	}
	app.Logf("client", "已连接服务端 %s，SOCKS5 端口 %d，空闲连接数 %d",
		s.client.serverAddress(), s.socksPort, s.poolSize)
	s.wg.Add(1)
	go s.fillLoop()
	s.wg.Add(1)
	go s.heartbeatLoop()
	s.readLoop()
	s.close()
	s.wg.Wait()
	return nil
}

// login 发送登录请求并校验结果
func (s *session) login() error {
	cfg := s.client.config()
	request := protocol.LoginRequest{
		ClientID: s.clientID,
		Name:     cfg.Name,
		Version:  app.Version(),
		Platform: app.Platform(),
		PoolSize: cfg.PoolSize,
		Mux:      true,
	}
	payload, err := protocol.EncodeJSON(request)
	if err != nil {
		return err
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if err := protocol.WriteFrame(s.conn, protocol.Frame{Type: protocol.FrameLogin, Payload: payload}); err != nil {
		return err
	}
	_ = s.conn.SetReadDeadline(time.Now().Add(loginTimeout))
	frame, err := protocol.ReadFrame(s.conn)
	if err != nil {
		return err
	}
	if frame.Type != protocol.FrameLoginResult {
		return errors.New("服务端未返回登录结果")
	}
	var result protocol.LoginResult
	if err := protocol.DecodeJSON(frame.Payload, &result); err != nil {
		return err
	}
	if !result.OK {
		if result.Error == "" {
			return errors.New("服务端拒绝登录")
		}
		return errors.New(result.Error)
	}
	if result.ClientID != "" {
		s.clientID = result.ClientID
	}
	s.socksPort = result.SocksPort
	s.poolSize = result.PoolSize
	if s.poolSize <= 0 {
		s.poolSize = s.client.config().PoolSize
	}
	_ = s.conn.SetReadDeadline(time.Time{})
	_ = s.conn.SetWriteDeadline(time.Time{})
	return nil
}

// readLoop 读取控制连接上的指令直到连接结束
//
// 新流不再由控制连接下发：服务端直接在空闲数据连接上写入建流指令
func (s *session) readLoop() {
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(heartbeatTimeout))
		frame, err := protocol.ReadFrame(s.conn)
		if err != nil {
			if !s.isClosed() {
				app.Logf("client", "控制连接结束: %v", err)
			}
			return
		}
		switch frame.Type {
		case protocol.FrameHeartbeat:
			s.lastPongNano.Store(time.Now().UnixNano())
			if err := s.send(protocol.Frame{Type: protocol.FrameHeartbeatAck}); err != nil {
				return
			}
		case protocol.FrameWorkConnRequest:
			var request protocol.WorkConnRequest
			if err := protocol.DecodeJSON(frame.Payload, &request); err != nil {
				app.Logf("client", "补池请求解析失败: %v", err)
				continue
			}
			s.enqueueFill(request.Count)
		default:
			// 其余帧不属于客户端处理范围，忽略
		}
	}
}

// enqueueFill 把服务端请求的补池数量交给补池协程
func (s *session) enqueueFill(count int) {
	if count <= 0 {
		return
	}
	if s.poolSize > 0 && count > s.poolSize {
		count = s.poolSize
	}
	select {
	case s.fillReqs <- count:
	case <-s.closed:
	default:
		// 队列已满说明补池协程正忙，本次请求丢弃后由服务端重新下发
	}
}

// send 在控制连接上串行写出一帧
func (s *session) send(frame protocol.Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.isClosed() {
		return errors.New("会话已结束")
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	err := protocol.WriteFrame(s.conn, frame)
	_ = s.conn.SetWriteDeadline(time.Time{})
	return err
}

// heartbeatLoop 定期发送心跳并检测链路是否已静默超时
func (s *session) heartbeatLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, s.lastPongNano.Load())) > heartbeatTimeout {
				app.Logf("client", "服务端心跳超时，重建连接")
				s.close()
				return
			}
			if err := s.send(protocol.Frame{Type: protocol.FrameHeartbeat}); err != nil {
				s.close()
				return
			}
		}
	}
}

// fillLoop 响应服务端的补池请求，建立空闲数据连接
func (s *session) fillLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.closed:
			return
		case count := <-s.fillReqs:
			if err := s.addIdleConn(count); err != nil {
				if !s.isClosed() {
					app.Logf("client", "建立空闲连接失败: %v", err)
				}
				select {
				case <-s.closed:
					return
				case <-time.After(fillRetryInterval):
				}
				s.enqueueFill(count - 1)
			}
		}
	}
}

// addIdleConn 建立指定数量的空闲数据连接并加入空闲池
func (s *session) addIdleConn(count int) error {
	added := 0
	for i := 0; i < count; i++ {
		conn, err := s.client.dialServer()
		if err != nil {
			if added == 0 {
				return err
			}
			break
		}
		if err := s.announceIdle(conn); err != nil {
			_ = conn.Close()
			if added == 0 {
				return err
			}
			break
		}
		s.mu.Lock()
		if s.isClosedLocked() {
			s.mu.Unlock()
			_ = conn.Close()
			break
		}
		s.idleConns = append(s.idleConns, conn)
		s.mu.Unlock()
		added++
		s.wg.Add(1)
		go s.serveIdle(conn)
	}
	return nil
}

// announceIdle 在新建的空闲连接上首帧上报归属
//
// 登录连接与空闲连接共用一个隧道端口，服务端按首帧类型区分
func (s *session) announceIdle(conn net.Conn) error {
	payload, err := protocol.EncodeJSON(protocol.NewWorkConn{
		ClientID: s.clientID,
		StreamID: protocol.MainStreamID,
		OK:       true,
	})
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if err := protocol.WriteFrame(conn, protocol.Frame{Type: protocol.FrameNewWorkConn, Payload: payload}); err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return nil
}

// serveIdle 等待服务端沿该数据连接下发建流指令
//
// 指令到达后本协程转为该隧道流的转发协程，连接所有权随之移交
func (s *session) serveIdle(conn net.Conn) {
	defer s.wg.Done()
	for {
		frame, err := protocol.ReadFrame(conn)
		if err != nil {
			s.dropIdle(conn)
			return
		}
		switch frame.Type {
		case protocol.FrameHeartbeat:
			// 服务端保活帧，继续等待建流指令
			continue
		case protocol.FrameNewStream:
			var request protocol.NewStreamRequest
			if err := protocol.DecodeJSON(frame.Payload, &request); err != nil {
				app.Logf("client", "建流指令解析失败: %v", err)
				s.dropIdle(conn)
				return
			}
			if request.Mux {
				s.promoteMux(conn, request)
				return
			}
			s.dispatchStream(conn, request)
			return
		default:
			s.dropIdle(conn)
			return
		}
	}
}

// dispatchStream 在指定数据连接上完成一次出网转发
func (s *session) dispatchStream(conn net.Conn, request protocol.NewStreamRequest) {
	// 转发期间连接不再属于空闲池，登记为活跃连接，会话结束时才能被统一关闭
	s.addBusy(conn)
	defer s.dropBusy(conn)
	target, reason, err := dialTarget(request.Host, request.Port, dialTimeout)
	if err != nil {
		s.reportFailure(conn, request.StreamID, reason, err)
		return
	}
	if err := s.handoff(conn, request, target); err != nil {
		app.Logf("client", "隧道流 %d 交付失败: %v", request.StreamID, err)
	}
}

// promoteMux 把空闲连接升级为多路复用载体，并把建流指令附带的首条流拨号交付
func (s *session) promoteMux(conn net.Conn, request protocol.NewStreamRequest) {
	target, reason, err := dialTarget(request.Host, request.Port, dialTimeout)
	if err != nil {
		// 首条流拨号失败就按普通建流失败回告，连接随之关闭，服务端会换一条空闲连接重试
		s.reportFailure(conn, request.StreamID, reason, err)
		return
	}
	raw := conn
	if request.Compress {
		conn = protocol.NewCompressConn(conn)
	}
	carrier := protocol.NewMuxSession(conn, s.handleMuxNewStream)
	// 先登记流再回告，服务端回告后立即写出的数据帧不会因为查无此流被丢弃
	stream := carrier.Open(request.StreamID)
	if stream == nil {
		_ = raw.Close()
		_ = target.Close()
		return
	}
	payload, err := protocol.EncodeJSON(protocol.NewWorkConn{
		ClientID: s.clientID,
		StreamID: request.StreamID,
		OK:       true,
		Compress: request.Compress,
		Mux:      true,
	})
	if err != nil {
		_ = raw.Close()
		_ = target.Close()
		return
	}
	_ = raw.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if err := protocol.WriteFrame(raw, protocol.Frame{Type: protocol.FrameNewWorkConn, Payload: payload}); err != nil {
		_ = raw.Close()
		_ = target.Close()
		return
	}
	_ = raw.SetWriteDeadline(time.Time{})
	// 回告之后连接离开空闲池，登记为在用连接，会话收尾时统一关闭
	s.addBusy(raw)
	// 转发协程独立运行，载体读取协程退出时流被关闭，转发随之结束
	go relay(stream, target)
	// Serve 返回即载体结束，会话收尾关闭连接后从这里退出
	carrier.Serve()
	s.dropBusy(raw)
}

// handleMuxNewStream 处理复用载体上的建流指令
//
// 由载体读取协程异步调用，拨号不会阻塞同一载体上的其他流
func (s *session) handleMuxNewStream(carrier *protocol.MuxSession, request protocol.NewStreamRequest) {
	s.serveMuxStream(carrier, request)
}

// serveMuxStream 在复用载体上完成单条流的拨号与转发
func (s *session) serveMuxStream(carrier *protocol.MuxSession, request protocol.NewStreamRequest) {
	target, reason, err := dialTarget(request.Host, request.Port, dialTimeout)
	if err != nil {
		message := protocol.NewWorkConn{
			ClientID: s.clientID,
			StreamID: request.StreamID,
			OK:       false,
			Reason:   reason,
			Error:    err.Error(),
		}
		s.replyMux(carrier, message)
		return
	}
	// 先登记流再回告，服务端回告后立即写出的数据帧不会因为查无此流被丢弃
	stream := carrier.Open(request.StreamID)
	if stream == nil {
		_ = target.Close()
		return
	}
	s.replyMux(carrier, protocol.NewWorkConn{ClientID: s.clientID, StreamID: request.StreamID, OK: true})
	relay(stream, target)
}

// replyMux 在复用载体上回告建流结果，载体已关闭时静默放弃
func (s *session) replyMux(carrier *protocol.MuxSession, message protocol.NewWorkConn) {
	payload, err := protocol.EncodeJSON(message)
	if err != nil {
		return
	}
	_ = carrier.WriteFrame(protocol.Frame{Type: protocol.FrameNewWorkConn, Payload: payload})
}

// dropIdle 从空闲池移除并关闭一条数据连接
func (s *session) dropIdle(conn net.Conn) {
	s.removeIdle(conn)
	_ = conn.Close()
}

// removeIdle 仅把连接从空闲池移除，连接本身继续由调用方使用
func (s *session) removeIdle(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeIdleLocked(conn)
}

// removeIdleLocked 在持有 s.mu 时把连接从空闲池移除
func (s *session) removeIdleLocked(conn net.Conn) {
	for i, item := range s.idleConns {
		if item == conn {
			s.idleConns = append(s.idleConns[:i], s.idleConns[i+1:]...)
			break
		}
	}
}

// addBusy 把连接移出空闲池并登记为正在转发的活跃连接
//
// 摘除与登记必须在一把锁内完成，否则会话收尾可能两边都看不到这条连接
func (s *session) addBusy(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeIdleLocked(conn)
	s.busyConns = append(s.busyConns, conn)
}

// dropBusy 把已结束的转发连接移出活跃连接集合
func (s *session) dropBusy(conn net.Conn) {
	s.mu.Lock()
	for i, item := range s.busyConns {
		if item == conn {
			s.busyConns = append(s.busyConns[:i], s.busyConns[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

// reportFailure 沿数据连接回告建流失败结果
func (s *session) reportFailure(conn net.Conn, streamID uint64, reason string, cause error) {
	defer conn.Close()
	message := protocol.NewWorkConn{
		ClientID: s.clientID,
		StreamID: streamID,
		OK:       false,
		Reason:   reason,
	}
	if cause != nil {
		message.Error = cause.Error()
	}
	payload, err := protocol.EncodeJSON(message)
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if err := protocol.WriteFrame(conn, protocol.Frame{Type: protocol.FrameNewWorkConn, Payload: payload}); err != nil {
		app.Logf("client", "回告建流失败结果出错: %v", err)
	}
}

// handoff 向服务端确认建流成功并把数据连接与本地目标连接对接
//
// 服务端在建流指令中请求压缩时回告同意，回告帧之后的数据按压缩通道转发
func (s *session) handoff(conn net.Conn, request protocol.NewStreamRequest, target net.Conn) error {
	message := protocol.NewWorkConn{ClientID: s.clientID, StreamID: request.StreamID, OK: true, Compress: request.Compress}
	payload, err := protocol.EncodeJSON(message)
	if err != nil {
		_ = conn.Close()
		_ = target.Close()
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if err := protocol.WriteFrame(conn, protocol.Frame{Type: protocol.FrameNewWorkConn, Payload: payload}); err != nil {
		_ = conn.Close()
		_ = target.Close()
		return err
	}
	_ = conn.SetWriteDeadline(time.Time{})
	if request.Compress {
		conn = protocol.NewCompressConn(conn)
	}
	relay(conn, target)
	return nil
}

// relay 在两个方向之间复制字节，任一端结束即整体关闭
func relay(left io.ReadWriteCloser, right io.ReadWriteCloser) {
	var once sync.Once
	closer := func() {
		_ = left.Close()
		_ = right.Close()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyBuffer(right, left)
		once.Do(closer)
	}()
	go func() {
		defer wg.Done()
		copyBuffer(left, right)
		once.Do(closer)
	}()
	wg.Wait()
}

// copyBuffer 单向复制数据，出错时静默结束
func copyBuffer(dst io.ReadWriteCloser, src io.ReadWriteCloser) {
	buffer := make([]byte, bufferSize)
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// isClosed 判断会话是否已结束
func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isClosedLocked()
}

// isClosedLocked 在已持有锁时判断会话是否已结束
func (s *session) isClosedLocked() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

// close 关闭会话及其持有的全部连接
//
// 空闲连接与正在转发的连接都要关闭，否则转发协程会一直卡在会话收尾上
func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.conn.Close()
		s.mu.Lock()
		idle := s.idleConns
		s.idleConns = nil
		busy := s.busyConns
		s.busyConns = nil
		s.mu.Unlock()
		for _, conn := range idle {
			_ = conn.Close()
		}
		for _, conn := range busy {
			_ = conn.Close()
		}
	})
}
