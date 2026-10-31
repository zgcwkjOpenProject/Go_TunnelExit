// session 实现单个隧道客户端的会话、空闲连接池与隧道流调度
//
// 服务端从池中取出一条空闲连接并下发建流指令，客户端回告结果后该连接转为数据通道
package server

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"tunnelexit/app"
	"tunnelexit/protocol"
	"tunnelexit/socks5"
)

// 会话级超时与重试参数
const (
	takeConnTimeout        = 20 * time.Second // 等待空闲数据连接或复用载体可用的最长时间
	streamHandshakeTimeout = 20 * time.Second // 等待客户端回告出网结果的最长时间
	muxCarrierTarget       = 2                // 多路复用模式下每会话维持的载体连接数
)

// errNoIdleConn 表示客户端没有及时提供空闲数据连接
var errNoIdleConn = errors.New("客户端暂无可用的空闲连接")

// ClientSession 表示一个已登录的隧道客户端
type ClientSession struct {
	server        *Server                // 所属服务端
	id            string                 // 客户端唯一标识，与来源 IP 一起构成客户端身份，也是端口记录与流量统计的归属键
	name          string                 // 客户端显示名
	clientIP      string                 // 控制连接的来源 IP，与 id 一起决定 SOCKS5 端口、校验空闲连接归属，并用于归属地展示
	version       string                 // 客户端版本
	platform      string                 // 客户端打包平台标识
	socksPort     int                    // 分配给该客户端的 SOCKS5 端口
	poolSize      int                    // 与客户端约定的空闲连接数
	supportsMux   bool                   // 登录时声明支持多路复用，决定空闲池的维持策略，协商失败时回退为 false
	conn          net.Conn               // 控制连接
	writeMu       sync.Mutex             // 串行化控制连接上的帧写入
	mu            sync.Mutex             // 保护 pool、busyConns、muxes 与 outstanding
	pool          []net.Conn             // 客户端预建的空闲数据连接，末尾优先取用
	busyConns     []net.Conn             // 已交由隧道流转发的数据连接，会话关闭时要与空闲连接一起关闭
	muxes         []*protocol.MuxSession // 已协商复用的数据连接载体，多条隧道流共享一条载体
	outstanding   int                    // 已下发补池请求、尚未进池的连接数
	notify        chan struct{}          // 出网请求等待连接的广播信号，唤醒时关闭并重建
	activeStreams atomic.Int64           // 正在转发的隧道流数量
	totalStreams  atomic.Uint64          // 会话累计处理的隧道流数量
	lastPongNano  atomic.Int64           // 最近一次心跳应答时间
	closeOnce     sync.Once              // 保证会话只关闭一次
	closed        chan struct{}          // 在会话关闭时关闭
}

// NewClientSession 创建客户端会话
func NewClientSession(srv *Server, id string, name string, clientIP string, version string, platform string, socksPort int, supportsMux bool, conn net.Conn) *ClientSession {
	session := &ClientSession{
		server:      srv,
		id:          id,
		name:        name,
		clientIP:    clientIP,
		version:     version,
		platform:    platform,
		socksPort:   socksPort,
		supportsMux: supportsMux,
		poolSize:    srv.cfg.PoolSize,
		conn:        conn,
		notify:      make(chan struct{}),
		closed:      make(chan struct{}),
	}
	session.lastPongNano.Store(time.Now().UnixNano())
	return session
}

// ID 返回客户端唯一标识
func (c *ClientSession) ID() string {
	return c.id
}

// Name 返回客户端显示名
func (c *ClientSession) Name() string {
	return c.name
}

// Version 返回客户端版本
func (c *ClientSession) Version() string {
	return c.version
}

// Platform 返回客户端打包平台标识
func (c *ClientSession) Platform() string {
	return c.platform
}

// SocksPort 返回分配的 SOCKS5 端口
func (c *ClientSession) SocksPort() int {
	return c.socksPort
}

// PoolSize 返回约定的空闲连接数
func (c *ClientSession) PoolSize() int {
	return c.poolSize
}

// IdleConns 返回当前空闲数据连接数
func (c *ClientSession) IdleConns() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pool)
}

// BusyConns 返回正在承载隧道流的数据连接数
func (c *ClientSession) BusyConns() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.busyConns)
}

// ActiveStreams 返回正在转发的隧道流数量
func (c *ClientSession) ActiveStreams() int {
	return int(c.activeStreams.Load())
}

// TotalStreams 返回会话累计处理的隧道流数量
func (c *ClientSession) TotalStreams() uint64 {
	return c.totalStreams.Load()
}

// State 返回会话在展示页面上的状态，空闲连接尚未补齐时算建立中
func (c *ClientSession) State() app.NodeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 复用会话的载体接管了全部空闲连接，载体在线即视为已建立
	if len(c.pool) == 0 && len(c.busyConns) == 0 && len(c.muxes) == 0 {
		return app.NodeStateEstablishing
	}
	return app.NodeStateOnline
}

// AddTraffic 把一条已结束的 SOCKS5 连接的双向流量计入本客户端
func (c *ClientSession) AddTraffic(up, down uint64) {
	c.server.store.AddTraffic(c.id, up, down)
}

// run 读取控制连接上的帧直到连接结束
func (c *ClientSession) run() {
	defer c.Close()
	c.maintainPool()
	for {
		frame, err := protocol.ReadFrame(c.conn)
		if err != nil {
			if !isClosedError(err) {
				app.Logf("server", "客户端 %s 控制连接中断: %v", c.name, err)
			}
			return
		}
		switch frame.Type {
		case protocol.FrameHeartbeat:
			// 客户端主动心跳时回复应答，便于双向探测链路
			if err := c.Send(protocol.Frame{Type: protocol.FrameHeartbeatAck}); err != nil {
				return
			}
		case protocol.FrameHeartbeatAck:
			c.lastPongNano.Store(time.Now().UnixNano())
		default:
			// 其他帧由各自主流程处理，这里忽略
		}
	}
}

// SendHeartbeat 向客户端发送一次心跳
func (c *ClientSession) SendHeartbeat() error {
	return c.Send(protocol.Frame{Type: protocol.FrameHeartbeat})
}

// Send 在控制连接上串行写出一帧
func (c *ClientSession) Send(frame protocol.Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.conn == nil {
		return ErrServerClosed
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := protocol.WriteFrame(c.conn, frame)
	_ = c.conn.SetWriteDeadline(time.Time{})
	return err
}

// lastPong 返回最近一次心跳应答时间
func (c *ClientSession) lastPong() time.Time {
	return time.Unix(0, c.lastPongNano.Load())
}

// AcceptIdleConn 接收客户端预建的空闲数据连接
func (c *ClientSession) AcceptIdleConn(conn net.Conn) {
	c.mu.Lock()
	if c.outstanding > 0 {
		c.outstanding--
	}
	closed := false
	select {
	case <-c.closed:
		closed = true
	default:
	}
	// 池已满或会话已关闭时关闭多余连接，避免连接数无上限增长
	if len(c.pool) >= c.poolSize || closed {
		c.mu.Unlock()
		_ = conn.Close()
		return
	}
	c.pool = append(c.pool, conn)
	c.mu.Unlock()
	c.wakeDial()
}

// releaseOutstanding 退回已预留但没能下发给客户端的补池额度
func (c *ClientSession) releaseOutstanding(count int) {
	c.mu.Lock()
	if c.outstanding >= count {
		c.outstanding -= count
	}
	c.mu.Unlock()
}

// maintainPool 按需向客户端下发补池请求
//
// 支持复用的会话按载体数维持少量连接，空闲连接也计入载体名额，多余的直接关闭；
// 不支持复用时按约定池深维持独立连接
func (c *ClientSession) maintainPool() {
	select {
	case <-c.closed:
		return
	default:
	}
	c.mu.Lock()
	target := c.poolSize
	if c.supportsMux {
		target = muxCarrierTarget
	}
	// 载体数可能超过目标值，空闲连接配额按零处理，避免负数下标
	poolTarget := target - len(c.muxes)
	if poolTarget < 0 {
		poolTarget = 0
	}
	for len(c.pool) > poolTarget {
		excess := c.pool[0]
		c.pool = c.pool[1:]
		_ = excess.Close()
	}
	missing := target - len(c.pool) - len(c.muxes) - c.outstanding
	if missing <= 0 {
		c.mu.Unlock()
		return
	}
	c.outstanding += missing
	c.mu.Unlock()
	payload, err := protocol.EncodeJSON(protocol.WorkConnRequest{Count: missing})
	if err != nil {
		// 请求没发出去就退回预留额度，否则这些连接永远等不到进池，空闲池再也不会补齐
		c.releaseOutstanding(missing)
		app.Logf("server", "客户端 %s 补充空闲连接请求编码失败: %v", c.name, err)
		return
	}
	if err := c.Send(protocol.Frame{Type: protocol.FrameWorkConnRequest, Payload: payload}); err != nil {
		c.releaseOutstanding(missing)
		app.Logf("server", "客户端 %s 补充空闲连接请求失败: %v", c.name, err)
	}
}

// wakeDial 广播唤醒所有等待连接的出网请求
//
// 复用载体登记后能同时服务多条流，必须唤醒全部等待者而不是只放行一个
func (c *ClientSession) wakeDial() {
	c.mu.Lock()
	close(c.notify)
	c.notify = make(chan struct{})
	c.mu.Unlock()
}

// tryIdle 立即取出一条空闲数据连接，池空时返回 nil
func (c *ClientSession) tryIdle() net.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pool) == 0 {
		return nil
	}
	last := len(c.pool) - 1
	conn := c.pool[last]
	c.pool[last] = nil
	c.pool = c.pool[:last]
	return conn
}

// Dial 通过客户端建立一条出网连接，实现 socks5.DialFunc
func (c *ClientSession) Dial(host string, port int) (socks5.Stream, error) {
	select {
	case <-c.closed:
		return nil, &socks5.ReplyError{Reply: socks5.ReplyGeneralFailure, Err: ErrServerClosed}
	default:
	}
	deadline := time.NewTimer(takeConnTimeout)
	defer deadline.Stop()
	var lastErr error
	// signal 是当前代的广播信号，唤醒后重取新代，避免在已关闭的通道上空转
	var signal chan struct{}
	for {
		if signal == nil {
			c.mu.Lock()
			signal = c.notify
			c.mu.Unlock()
		}
		// 已有复用载体时优先在其上开流，省去每条流一次的连接握手
		for {
			carrier := c.peekMux()
			if carrier == nil {
				break
			}
			stream, broken, err := c.openMuxStream(carrier, host, port)
			if !broken {
				if err == nil {
					c.totalStreams.Add(1)
					c.activeStreams.Add(1)
				}
				return stream, err
			}
			// 载体已失效，摘除后换下一条兜底
			c.dropMux(carrier)
		}
		conn := c.tryIdle()
		if conn == nil {
			// 空闲连接入池或新载体登记都会广播唤醒，等待期间会话关闭则直接失败
			select {
			case <-c.closed:
				return nil, &socks5.ReplyError{Reply: socks5.ReplyGeneralFailure, Err: ErrServerClosed}
			case <-signal:
				signal = nil
				continue
			case <-deadline.C:
				if lastErr == nil {
					lastErr = errNoIdleConn
				}
				return nil, &socks5.ReplyError{Reply: socks5.ReplyHostUnreachable, Err: lastErr}
			}
		}
		signal = nil
		// 连接已被取出，立即请求补足以保持池深度稳定
		c.maintainPool()
		stream, retry, err := c.startStream(conn, host, port)
		if !retry {
			if err == nil {
				c.totalStreams.Add(1)
				c.activeStreams.Add(1)
			}
			return stream, err
		}
		lastErr = err
		c.maintainPool()
	}
}

// startStream 在一条空闲连接上发起隧道流
//
// 返回 retry 为 true 表示该连接疑似失效，调用方应换用其他空闲连接重试
func (c *ClientSession) startStream(conn net.Conn, host string, port int) (socks5.Stream, bool, error) {
	streamID := c.server.nextStreamID()
	// 载体数已达目标时改走独立连接，避免并发建流把载体撑爆后空闲池再也补不回来
	c.mu.Lock()
	wantMux := c.supportsMux && len(c.muxes) < muxCarrierTarget
	c.mu.Unlock()
	// 请求对这条流启用压缩与多路复用，旧客户端不认识这些字段会原样回告未设置，此时按明文独立连接转发
	payload, err := protocol.EncodeJSON(protocol.NewStreamRequest{StreamID: streamID, Host: host, Port: port, Compress: true, Mux: wantMux})
	if err != nil {
		_ = conn.Close()
		return nil, false, &socks5.ReplyError{Reply: socks5.ReplyGeneralFailure, Err: err}
	}
	_ = conn.SetDeadline(time.Now().Add(streamHandshakeTimeout))
	if err := protocol.WriteFrame(conn, protocol.Frame{Type: protocol.FrameNewStream, Payload: payload}); err != nil {
		_ = conn.Close()
		return nil, true, err
	}
	for {
		frame, err := protocol.ReadFrame(conn)
		if err != nil {
			_ = conn.Close()
			return nil, true, err
		}
		if frame.Type != protocol.FrameNewWorkConn {
			continue
		}
		var message protocol.NewWorkConn
		if err := protocol.DecodeJSON(frame.Payload, &message); err != nil {
			_ = conn.Close()
			return nil, true, err
		}
		if message.StreamID != streamID {
			continue
		}
		_ = conn.SetDeadline(time.Time{})
		if message.OK {
			// 请求了复用而客户端未回告复用时，整段会话退回独立连接模式
			if wantMux && !message.Mux {
				c.mu.Lock()
				c.supportsMux = false
				c.mu.Unlock()
			}
			if message.Compress {
				conn = protocol.NewCompressConn(conn)
			}
			if message.Mux {
				return c.promoteMux(conn, streamID)
			}
			c.addBusy(conn)
			return &trackedStream{Conn: conn, session: c}, false, nil
		}
		_ = conn.Close()
		c.maintainPool()
		reason := message.Reason
		if reason == "" {
			reason = protocol.ReasonOther
		}
		detail := message.Error
		if detail == "" {
			detail = "客户端建立出网连接失败"
		}
		return nil, false, &socks5.ReplyError{Reply: ReplyForReason(reason), Err: errors.New(detail)}
	}
}

// trackedStream 包装隧道连接，在关闭时递减活跃流计数并补充空闲连接
type trackedStream struct {
	// Conn 是客户端交付的隧道连接
	net.Conn
	// session 指向所属会话
	session *ClientSession
	// once 保证收尾只执行一次
	once sync.Once
}

// Close 关闭隧道连接并完成会话侧计数与补池
func (t *trackedStream) Close() error {
	err := t.Conn.Close()
	t.once.Do(func() {
		t.session.activeStreams.Add(-1)
		t.session.removeBusy(t.Conn)
		t.session.maintainPool()
	})
	return err
}

// promoteMux 把已协商复用的连接升级为载体并交付首条流
func (c *ClientSession) promoteMux(conn net.Conn, streamID uint64) (socks5.Stream, bool, error) {
	carrier := protocol.NewMuxSession(conn, nil)
	stream := carrier.Open(streamID)
	if stream == nil {
		_ = carrier.Close()
		return nil, true, nil
	}
	c.mu.Lock()
	c.muxes = append(c.muxes, carrier)
	c.mu.Unlock()
	// 新载体能同时服务多条流，广播唤醒正在等待连接的出网请求
	c.wakeDial()
	// 载体读取协程退出即连接失效，摘除后重新维持载体数量
	go func() {
		carrier.Serve()
		c.dropMux(carrier)
		c.maintainPool()
	}()
	return &muxTrackedStream{MuxStream: stream, session: c}, false, nil
}

// openMuxStream 在复用载体上发起一条隧道流并等待客户端回告
//
// broken 为 true 表示载体已失效，调用方应摘除该载体换路重试
func (c *ClientSession) openMuxStream(carrier *protocol.MuxSession, host string, port int) (socks5.Stream, bool, error) {
	streamID := c.server.nextStreamID()
	payload, err := protocol.EncodeJSON(protocol.NewStreamRequest{StreamID: streamID, Host: host, Port: port, Compress: true, Mux: true})
	if err != nil {
		return nil, false, &socks5.ReplyError{Reply: socks5.ReplyGeneralFailure, Err: err}
	}
	if err := carrier.WriteFrame(protocol.Frame{Type: protocol.FrameNewStream, Payload: payload}); err != nil {
		return nil, true, nil
	}
	// 先登记流再等待回告，客户端先于回告送达的数据帧不会因为查无此流而被丢弃
	stream := carrier.Open(streamID)
	if stream == nil {
		return nil, true, nil
	}
	message, err := carrier.WaitReply(streamID, streamHandshakeTimeout)
	if err != nil {
		carrier.Discard(streamID)
		return nil, true, nil
	}
	if !message.OK {
		carrier.Discard(streamID)
		reason := message.Reason
		if reason == "" {
			reason = protocol.ReasonOther
		}
		detail := message.Error
		if detail == "" {
			detail = "客户端建立出网连接失败"
		}
		return nil, false, &socks5.ReplyError{Reply: ReplyForReason(reason), Err: errors.New(detail)}
	}
	return &muxTrackedStream{MuxStream: stream, session: c}, false, nil
}

// peekMux 查看一条复用载体但不摘除，失效时由调用方显式摘除
func (c *ClientSession) peekMux() *protocol.MuxSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.muxes) == 0 {
		return nil
	}
	return c.muxes[0]
}

// dropMux 摘除并关闭一条复用载体
func (c *ClientSession) dropMux(carrier *protocol.MuxSession) {
	c.mu.Lock()
	for i, item := range c.muxes {
		if item == carrier {
			c.muxes = append(c.muxes[:i], c.muxes[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	_ = carrier.Close()
}

// muxTrackedStream 包装复用流，在关闭时递减活跃流计数并维持载体数量
//
// 载体连接本身不随单条流关闭，只关流并发送结束帧
type muxTrackedStream struct {
	// MuxStream 是复用载体上的一条流
	*protocol.MuxStream
	// session 指向所属会话
	session *ClientSession
	// once 保证收尾只执行一次
	once sync.Once
}

// Close 关闭流并完成会话侧计数
func (t *muxTrackedStream) Close() error {
	err := t.MuxStream.Close()
	t.once.Do(func() {
		t.session.activeStreams.Add(-1)
		t.session.maintainPool()
	})
	return err
}

// addBusy 把开始承载隧道流的连接登记为活跃连接
func (c *ClientSession) addBusy(conn net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busyConns = append(c.busyConns, conn)
}

// removeBusy 把已结束的隧道连接移出活跃连接集合
func (c *ClientSession) removeBusy(conn net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, item := range c.busyConns {
		if item == conn {
			c.busyConns = append(c.busyConns[:i], c.busyConns[i+1:]...)
			break
		}
	}
}

// Close 关闭会话并释放其占用的端口与连接
func (c *ClientSession) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.conn != nil {
			_ = c.conn.Close()
		}
		c.mu.Lock()
		conns := c.pool
		c.pool = nil
		// 正在转发的连接已经不在池里，必须一起关闭，否则会留下半开的转发
		busy := c.busyConns
		c.busyConns = nil
		muxes := c.muxes
		c.muxes = nil
		c.mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		for _, conn := range busy {
			_ = conn.Close()
		}
		for _, carrier := range muxes {
			_ = carrier.Close()
		}
		c.server.release(c)
	})
}

// ReplyForReason 把客户端上报的失败原因映射为 SOCKS5 应答码
func ReplyForReason(reason string) byte {
	switch reason {
	case protocol.ReasonRefused:
		return socks5.ReplyConnectionRefused
	case protocol.ReasonTimeout:
		return socks5.ReplyTTLExpired
	case protocol.ReasonUnreachable:
		return socks5.ReplyHostUnreachable
	default:
		return socks5.ReplyGeneralFailure
	}
}

// isClosedError 判断错误是否属于连接已关闭的正常结束
func isClosedError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, ErrServerClosed) || errors.Is(err, io.EOF)
}
