// server 实现隧道服务端：客户端登录、SOCKS5 端口分配与流调度
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tunnelexit/app"
	"tunnelexit/geoip"
	"tunnelexit/protocol"
	"tunnelexit/socks5"
)

// 服务端运行参数
const (
	loginTimeout      = 15 * time.Second // 客户端登录帧的读取超时
	heartbeatInterval = 30 * time.Second // 向客户端发送心跳的间隔
	heartbeatTimeout  = 90 * time.Second // 连续未收到心跳应答后判定客户端离线的时长
)

// ErrServerClosed 表示服务端已停止，不再接受新请求
var ErrServerClosed = errors.New("隧道服务端已停止")

// ErrClientAddressBusy 表示同一客户端 ID 正从其他来源地址在线，本次登录被拒绝
//
// 客户端身份由 ID 与来源 IP 共同确定，复制客户端配置到别的机器顶不掉正在运行的真客户端
var ErrClientAddressBusy = errors.New("该客户端 ID 正在其他来源地址上运行，待它断开后即可重新接入")

// clientListener 是分配给某个客户端的 SOCKS5 监听
type clientListener struct {
	port     int          // 监听端口
	listener net.Listener // 监听句柄
	clientID string       // 当前占用该端口的客户端标识
}

// Server 是隧道服务端，协调客户端会话与 SOCKS5 端口
//
// 客户端身份由唯一 ID 与来源 IP 共同确定，SOCKS5 端口按这对组合固定分配；
// 隧道端口只接受 TLS 连接，非法或未加密的连接在握手段即被丢弃
type Server struct {
	cfg       app.ServerConfig          // 启动时确定的服务端配置
	store     *NodeStore                // 客户端与 SOCKS5 端口、流量统计的映射
	geo       *geoip.Library            // 离线归属地库集合，某个版本未装载时该地址族按未收录处理
	key       tls.Certificate           // 服务端密钥对，隧道端口按它做 TLS 握手
	mu        sync.Mutex                // 保护 sessions 与 listeners
	sessions  map[string]*ClientSession // 按客户端 ID 保存活跃会话
	listeners map[int]*clientListener   // 按端口保存 SOCKS5 监听
	streamSeq atomic.Uint64             // 分配全局唯一的隧道流标识
	listener  net.Listener              // 隧道端口监听，承载登录与空闲数据连接
	closed    atomic.Bool               // 标记服务端是否已停止
}

// New 创建隧道服务端
//
// 密钥对由调用方提前装载，装载失败时无法建立加密隧道，因此这里要求必须传入
func New(cfg app.ServerConfig, appDir string, certificate tls.Certificate) *Server {
	store := NewNodeStore(filepath.Join(appDir, app.ClientsStoreFile))
	if err := store.Load(); err != nil {
		app.Logf("server", "读取节点记录失败，将重新分配端口: %v", err)
	}
	return &Server{
		cfg:       cfg,
		store:     store,
		geo:       openGeoIP(appDir),
		key:       certificate,
		sessions:  make(map[string]*ClientSession),
		listeners: make(map[int]*clientListener),
	}
}

// openGeoIP 装载 IPv4 与 IPv6 两份离线归属地库
//
// 两份库各自缺失或损坏只影响对应地址族的展示，服务端只记日志不退出
func openGeoIP(appDir string) *geoip.Library {
	library := geoip.NewLibrary()
	for _, path := range []string{app.GeoIPPath(appDir), app.GeoIPPathV6(appDir)} {
		if err := library.Load(path); err != nil {
			app.Logf("server", "归属地库 %s 不可用，该地址族的来源显示未知地区: %v", path, err)
			continue
		}
		app.Logf("server", "归属地库已加载 %s", path)
	}
	return library
}

// Config 返回服务端当前配置
func (s *Server) Config() app.ServerConfig {
	return s.cfg
}

// Start 启动隧道端口与后台维护协程
func (s *Server) Start(ctx context.Context) error {
	// 通配地址在支持 IPv4 映射的系统上落到双栈套接字，IPv4 与 IPv6 客户端都能接入；
	// 系统完全关闭 IPv6 时自动退回仅 IPv4，因此不需要分别监听两个地址族
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(s.cfg.Port))
	plain, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听隧道端口 %s 失败: %w", addr, err)
	}
	// 隧道端口一律走 TLS：没有配对密钥的客户端无法通过握手，协议帧只会在加密通道里出现
	listener := tls.NewListener(plain, app.ServerTLSConfig(s.key))
	s.listener = listener
	app.Logf("server", "隧道端口已监听 %s 并启用 TLS，SOCKS5 端口范围 %d-%d",
		addr, s.cfg.SocksPortBase, s.cfg.SocksPortBase+s.cfg.SocksPortCount-1)

	// 启动时先按保留天数清理长期未在线的记录，再开始接受客户端
	s.pruneNodes(time.Now())
	s.saveNodes()

	// 握手失败的连接只记日志，不进入协议层
	go s.acceptLoop(listener, s.handleConn)
	go s.maintain(ctx)
	return nil
}

// Stop 关闭全部监听与客户端会话
func (s *Server) Stop() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.mu.Lock()
	sessions := make([]*ClientSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	listeners := make([]*clientListener, 0, len(s.listeners))
	for _, item := range s.listeners {
		listeners = append(listeners, item)
	}
	s.sessions = make(map[string]*ClientSession)
	s.listeners = make(map[int]*clientListener)
	s.mu.Unlock()
	for _, item := range listeners {
		_ = item.listener.Close()
	}
	for _, session := range sessions {
		session.Close()
	}
	// 会话关闭后补一次落盘，尽量保留关闭前的流量统计
	if err := s.store.Save(); err != nil {
		app.Logf("server", "保存节点记录失败: %v", err)
	}
	_ = s.geo.Close()
}

// acceptLoop 持续接受连接并交给处理函数
func (s *Server) acceptLoop(listener net.Listener, handler func(net.Conn)) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			app.Logf("server", "接受连接失败: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetKeepAlive(true)
			_ = tcp.SetKeepAlivePeriod(30 * time.Second)
		}
		go handler(conn)
	}
}

// handleConn 按首帧类型区分同一条隧道端口上的登录连接与空闲数据连接
func (s *Server) handleConn(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(loginTimeout))
	frame, err := protocol.ReadFrame(conn)
	if err != nil {
		app.Logf("server", "隧道连接首帧读取失败: %v", err)
		_ = conn.Close()
		return
	}
	switch frame.Type {
	case protocol.FrameLogin:
		s.handleLogin(conn, frame)
	case protocol.FrameNewWorkConn:
		s.handleIdleConn(conn, frame)
	default:
		app.Logf("server", "隧道连接首帧类型 %d 不受支持", frame.Type)
		_ = conn.Close()
	}
}

// handleLogin 处理客户端登录连接
func (s *Server) handleLogin(conn net.Conn, frame protocol.Frame) {
	var request protocol.LoginRequest
	if err := protocol.DecodeJSON(frame.Payload, &request); err != nil {
		app.Logf("server", "登录消息解析失败: %v", err)
		_ = conn.Close()
		return
	}
	if !validClientID(request.ClientID) {
		s.writeLoginResult(conn, protocol.LoginResult{OK: false, Error: "客户端缺少合法的唯一 ID，请升级客户端"})
		app.Logf("server", "客户端 %s 缺少合法的唯一 ID，已拒绝登录", request.Name)
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	session, err := s.register(conn, request)
	if err != nil {
		s.writeLoginResult(conn, protocol.LoginResult{OK: false, Error: err.Error()})
		app.Logf("server", "客户端 %s 注册失败: %v", request.Name, err)
		_ = conn.Close()
		return
	}
	result := protocol.LoginResult{
		OK:        true,
		ClientID:  session.id,
		SocksPort: session.socksPort,
		PoolSize:  s.cfg.PoolSize,
	}
	if err := s.writeLoginResult(conn, result); err != nil {
		app.Logf("server", "下发登录结果失败: %v", err)
		session.Close()
		return
	}
	app.Logf("server", "客户端 %s 已连接，ID %s，来源 %s，SOCKS5 端口 %d",
		session.name, session.id, session.clientIP, session.socksPort)
	session.run()
}

// validClientID 校验客户端 ID 是否合法，避免异常取值写进端口记录
func validClientID(id string) bool {
	// 客户端 ID 由客户端自身生成，服务端只做长度与字符范围检查
	if id == "" || len(id) > 64 {
		return false
	}
	for _, char := range id {
		switch {
		case char >= '0' && char <= '9':
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char == '-' || char == '_':
		default:
			return false
		}
	}
	return true
}

// writeLoginResult 向客户端回告登录结果
func (s *Server) writeLoginResult(conn net.Conn, result protocol.LoginResult) error {
	payload, err := protocol.EncodeJSON(result)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(loginTimeout))
	return protocol.WriteFrame(conn, protocol.Frame{Type: protocol.FrameLoginResult, Payload: payload})
}

// register 登记客户端会话并为其准备 SOCKS5 监听
//
// 同一 ID 从原地址重连时新会话接管旧会话并复用原端口，旧会话仍在线时来自其他地址的登录一律拒绝
func (s *Server) register(conn net.Conn, request protocol.LoginRequest) (*ClientSession, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = "client"
	}
	address := remoteAddress(conn)
	s.mu.Lock()
	existing, online := s.sessions[request.ClientID]
	// 旧会话还在时只认原来源地址，否则拿到客户端 ID 的人都能从任意机器顶掉真客户端
	if online && existing.clientIP != address {
		s.mu.Unlock()
		app.Logf("server", "客户端 %s（ID %s）正从 %s 在线，拒绝来自 %s 的同 ID 登录",
			existing.name, request.ClientID, existing.clientIP, address)
		return nil, ErrClientAddressBusy
	}
	// 同 ID 从原地址重连时先摘掉旧会话与其端口，避免同一客户端占用两个端口
	//
	// 这里只从映射中摘除并关掉监听，真正的会话关闭必须放到解锁之后：
	// Close 会回调 release 再去抢同一把锁，锁内关闭会直接死锁
	var stale []*ClientSession
	if online {
		delete(s.sessions, request.ClientID)
		stale = append(stale, existing)
		if item, ok := s.listeners[existing.socksPort]; ok {
			_ = item.listener.Close()
			delete(s.listeners, existing.socksPort)
		}
	}
	port, err := s.allocatePortLocked(request.ClientID, address, name)
	if err != nil {
		s.mu.Unlock()
		closeSessions(stale)
		return nil, err
	}
	// 分配已经改动记录，落盘统一放在解锁之后，避免持锁做磁盘写入
	listener, err := net.Listen("tcp", net.JoinHostPort(s.cfg.SocksHost, strconv.Itoa(port)))
	if err != nil {
		s.mu.Unlock()
		s.saveNodes()
		closeSessions(stale)
		return nil, fmt.Errorf("监听 SOCKS5 端口 %d 失败: %w", port, err)
	}
	id := request.ClientID
	session := NewClientSession(s, id, name, address, request.Version, request.Platform, port, request.Mux, conn)
	s.sessions[id] = session
	s.listeners[port] = &clientListener{port: port, listener: listener, clientID: id}
	s.mu.Unlock()
	s.saveNodes()
	closeSessions(stale)
	go s.serveSocks(listener, session)
	return session, nil
}

// closeSessions 在不持有 s.mu 的前提下批量关闭会话
func closeSessions(sessions []*ClientSession) {
	for _, session := range sessions {
		session.Close()
	}
}

// serveSocks 接受某客户端 SOCKS5 端口上的连接
func (s *Server) serveSocks(listener net.Listener, session *ClientSession) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			app.Logf("server", "SOCKS5 端口 %d 接受连接失败: %v", session.socksPort, err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetKeepAlive(true)
			_ = tcp.SetKeepAlivePeriod(30 * time.Second)
		}
		go s.handleSocks(conn, session)
	}
}

// handleSocks 处理一条 SOCKS5 连接并把出网请求交给对应客户端
//
// 外部客户端与目标之间的双向数据都经过这条连接，因此在这里统计双向流量
func (s *Server) handleSocks(conn net.Conn, session *ClientSession) {
	remote := conn.RemoteAddr().String()
	traffic := &trafficConn{Conn: conn}
	err := socks5.Handle(traffic, session.Dial)
	_ = conn.Close()
	session.AddTraffic(traffic.up.Load(), traffic.down.Load())
	if err != nil {
		app.Logf("server", "客户端 %s 的 SOCKS5 请求 %s 结束: %v", session.name, remote, err)
	}
}

// trafficConn 包装对外 SOCKS5 连接并统计双向字节数
//
// 只嵌入 net.Conn，不实现 ReaderFrom 或 WriterTo，使 io.Copy 的两个方向都落到下面的读写方法上
type trafficConn struct {
	net.Conn               // 原始连接
	up       atomic.Uint64 // 上行字节数，外部客户端发往目标
	down     atomic.Uint64 // 下行字节数，目标返回给外部客户端
}

// Read 读取外部客户端数据并计入上行
func (t *trafficConn) Read(buffer []byte) (int, error) {
	read, err := t.Conn.Read(buffer)
	if read > 0 {
		t.up.Add(uint64(read))
	}
	return read, err
}

// Write 向外写回目标数据并计入下行
func (t *trafficConn) Write(buffer []byte) (int, error) {
	written, err := t.Conn.Write(buffer)
	if written > 0 {
		t.down.Add(uint64(written))
	}
	return written, err
}

// remoteAddress 取连接的来源 IP，解析失败时退回原始地址
//
// 双栈监听下 IPv4 客户端可能以 IPv4 映射地址呈现，归一化后记录、判重与展示都是同一种写法
func remoteAddress(conn net.Conn) string {
	address := conn.RemoteAddr().String()
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return normalizeAddress(host)
}

// normalizeAddress 把地址归一化为标准写法，无法解析时原样返回
func normalizeAddress(address string) string {
	ip := net.ParseIP(address)
	if ip == nil {
		return address
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ip.String()
}

// handleIdleConn 接纳客户端预建的空闲数据连接
//
// 空闲连接与登录连接共用隧道端口，只有来源地址与该会话绑定的地址一致的连接才会被接纳
func (s *Server) handleIdleConn(conn net.Conn, frame protocol.Frame) {
	var message protocol.NewWorkConn
	if err := protocol.DecodeJSON(frame.Payload, &message); err != nil {
		app.Logf("server", "空闲连接归属消息解析失败: %v", err)
		_ = conn.Close()
		return
	}
	if message.StreamID != protocol.MainStreamID {
		app.Logf("server", "空闲连接首帧的流标识 %d 不是控制流标识", message.StreamID)
		_ = conn.Close()
		return
	}
	session := s.sessionByID(message.ClientID)
	if session == nil {
		app.Logf("server", "空闲连接找不到客户端 %s 的会话", message.ClientID)
		_ = conn.Close()
		return
	}
	// 空闲连接与登录连接必须来自同一地址，否则拿客户端 ID 就能给别人的会话补连接
	if address := remoteAddress(conn); address != session.clientIP {
		app.Logf("server", "客户端 %s 的空闲连接来自 %s，与该会话绑定的 %s 不一致，已拒绝",
			message.ClientID, address, session.clientIP)
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	session.AcceptIdleConn(conn)
}

// sessionByID 按客户端 ID 查找活跃会话，未登录的 ID 返回 nil
func (s *Server) sessionByID(id string) *ClientSession {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// allocatePortLocked 为客户端分配 SOCKS5 端口，优先复用历史分配
//
// 端口只能落在配置的范围内，分配可能改动记录，落盘交由调用方在解锁后执行
func (s *Server) allocatePortLocked(clientID string, address string, name string) (int, error) {
	used := make(map[int]bool, len(s.listeners))
	for port := range s.listeners {
		used[port] = true
	}
	port, err := s.store.Reserve(clientID, address, name, s.cfg.SocksPortBase, s.cfg.SocksPortCount,
		func(candidate int) bool {
			// 已被其他会话监听或本机无法监听的端口均不可用
			if used[candidate] {
				return true
			}
			return !portAvailable(s.cfg.SocksHost, candidate)
		})
	if err != nil {
		return 0, err
	}
	return port, nil
}

// portAvailable 探测端口是否可被本机监听
func portAvailable(host string, port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// nextStreamID 分配一个全局唯一的隧道流标识
func (s *Server) nextStreamID() uint64 {
	return s.streamSeq.Add(1)
}

// release 移除已断开的客户端会话，保留端口记录供同一客户端重连复用
func (s *Server) release(session *ClientSession) {
	s.mu.Lock()
	if current, ok := s.sessions[session.id]; ok && current == session {
		delete(s.sessions, session.id)
		if item, ok := s.listeners[session.socksPort]; ok && item.clientID == session.id {
			_ = item.listener.Close()
			delete(s.listeners, session.socksPort)
		}
		app.Logf("server", "客户端 %s（ID %s）已断开，端口 %d 已释放，端口记录保留供重连复用",
			session.name, session.id, session.socksPort)
	}
	s.mu.Unlock()
	s.store.Touch(session.id, time.Now())
	s.saveNodes()
}

// saveNodes 落盘节点记录，只能在不持有 s.mu 时调用
//
// 记录写入涉及磁盘 I/O，放在锁内会连带阻塞登录与会话收尾；停止后不再落盘，避免与 Stop 的收尾写入争抢文件
func (s *Server) saveNodes() {
	if s.closed.Load() {
		return
	}
	if err := s.store.Save(); err != nil {
		app.Logf("server", "保存节点记录失败: %v", err)
	}
}

// pruneNodes 按保留天数清理长期未在线的节点记录
func (s *Server) pruneNodes(now time.Time) {
	if removed := s.store.Prune(s.cfg.NodeKeepDays, now); removed > 0 {
		app.Logf("server", "已清理 %d 条超过 %d 天未在线的节点记录", removed, s.cfg.NodeKeepDays)
	}
}

// ActiveClientCount 返回当前在线客户端数量
func (s *Server) ActiveClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Nodes 汇总全部节点状态，供节点展示页展示在线、建立中与离线节点
//
// 结果按状态分组，组内按 SOCKS5 端口升序
// 返回结果含来源 IP 与名称，属于可识别数据，公开接口必须只取其匿名字段
func (s *Server) Nodes() []app.NodeStatus {
	records := s.store.Snapshot()
	s.mu.Lock()
	sessions := make([]*ClientSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	// 先按持久化记录铺开，再用手上的会话覆盖在线状态，两类节点都能出现
	byID := make(map[string]app.NodeStatus, len(records)+len(sessions))
	for id, record := range records {
		place, region := s.regionOf(record.Address)
		byID[id] = app.NodeStatus{
			ID:       id,
			Address:  record.Address,
			Name:     record.Name,
			Port:     record.Port,
			State:    app.NodeStateOffline,
			Place:    place,
			Region:   region,
			Up:       record.Up,
			Down:     record.Down,
			LastSeen: record.LastSeen,
		}
	}
	for _, session := range sessions {
		status := byID[session.id]
		status.ID = session.id
		status.Address = session.clientIP
		status.Name = session.name
		status.Port = session.socksPort
		status.State = session.State()
		status.ActiveStreams = session.ActiveStreams()
		status.TotalStreams = session.TotalStreams()
		status.LastSeen = time.Now()
		byID[session.id] = status
	}
	statuses := make([]app.NodeStatus, 0, len(byID))
	for _, status := range byID {
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].State != statuses[j].State {
			return statusRank(statuses[i].State) < statusRank(statuses[j].State)
		}
		return statuses[i].Port < statuses[j].Port
	})
	return statuses
}

// statusRank 返回节点状态在列表里的排序权重，在线节点排在离线节点前面
func statusRank(state app.NodeState) int {
	switch state {
	case app.NodeStateOnline:
		return 0
	case app.NodeStateEstablishing:
		return 1
	default:
		return 2
	}
}

// PrivateRegion 是内网与本机来源地址的归属地展示文本
//
// 这类地址不在归属地库的收录范围内，直接查库只能得到保留值，对外展示局域网更有意义
const PrivateRegion = "局域网"

// regionOf 返回来源地址的底图落点键与归属地展示文本，内网与保留地址没有落点
func (s *Server) regionOf(address string) (string, string) {
	if isPrivateAddress(address) {
		return "", PrivateRegion
	}
	if region, ok := s.geo.Lookup(address); ok {
		return region.Place(), region.Display()
	}
	return "", geoip.UnknownRegion
}

// isPrivateAddress 判断地址是否属于本机、内网或链路本地范围，这类地址无需查库
func isPrivateAddress(address string) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// maintain 周期检查心跳并回收超时会话
func (s *Server) maintain(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.closed.Load() {
				return
			}
			s.mu.Lock()
			sessions := make([]*ClientSession, 0, len(s.sessions))
			for _, session := range s.sessions {
				sessions = append(sessions, session)
			}
			s.mu.Unlock()
			now := time.Now()
			for _, session := range sessions {
				if now.Sub(session.lastPong()) > heartbeatTimeout {
					app.Logf("server", "客户端 %s 心跳超时，主动断开", session.name)
					session.Close()
					continue
				}
				if err := session.SendHeartbeat(); err != nil {
					app.Logf("server", "客户端 %s 心跳发送失败: %v", session.name, err)
					session.Close()
					continue
				}
				s.store.Touch(session.id, now)
			}
			// 会话刚刷新过最近在线时间，不会被清理
			s.pruneNodes(now)
			// 随心跳周期落盘，进程被强杀时最多丢一个周期的流量统计
			if err := s.store.Save(); err != nil {
				app.Logf("server", "保存节点记录失败: %v", err)
			}
		}
	}
}
