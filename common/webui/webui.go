// webui 提供 Web 界面：服务端只对外展示节点状态，客户端只提供本机配置管理，且不反向引用 server 与 client
package webui

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tunnelexit/app"
)

// Mode 标记界面由服务端还是客户端拉起，两个角色挂载的路由完全不重叠
type Mode string

// 界面角色取值
const (
	ModeServer Mode = "server" // 服务端界面，只提供匿名节点展示
	ModeClient Mode = "client" // 客户端界面，只提供本机配置管理
)

// 界面运行参数
const (
	tokenCookie     = "tunnelexit_web_token" // 口令校验通过后写入的 Cookie 名，避免后续请求反复携带查询参数
	readTimeout     = 10 * time.Second       // 读取请求头的超时
	writeTimeout    = 30 * time.Second       // 写出响应的超时，留足时间用于输出较长的日志尾部
	maxConfigBody   = 64 * 1024              // 配置请求体大小上限
	maxLogLines     = 2000                   // 单次返回的日志行数上限
	defaultLogLines = 200                    // 未指定行数时返回的日志行数
	streamInterval  = time.Second            // 实时推送两轮之间的间隔
)

// Options 是 Web 界面的启动参数
type Options struct {
	Mode          Mode                    // 决定界面提供公开展示还是本机配置管理
	AppDir        string                  // 配置与日志所在目录
	Host          string                  // 监听地址
	Port          int                     // 监听端口
	Socket        string                  // 套件网关接入的 unix socket 路径，非空时只监听该 socket
	Path          string                  // 界面挂载的路径前缀，根路径表示挂在监听端口根上
	Token         string                  // 非空时要求访问者携带同一口令，只保护客户端的配置页面与接口
	Nodes         func() []app.NodeStatus // 返回服务端全部节点状态，由入口注入
	OnConfigSaved func()                  // 配置页写入新配置后的回调，由客户端入口注入用于立刻生效
}

// UI 是 Web 界面的 HTTP 服务
type UI struct {
	opts     Options      // 启动参数，监听建立后不再变化
	listener net.Listener // 已绑定的监听器，端口取 0 时由系统分配，需要读回真实地址
	server   *http.Server // 底层 HTTP 服务
}

// New 创建 Web 界面
func New(opts Options) *UI {
	return &UI{opts: opts}
}

// nodes 返回入口注入的节点状态，未注入时按空列表处理
func (u *UI) nodes() []app.NodeStatus {
	if u.opts.Nodes == nil {
		return nil
	}
	return u.opts.Nodes()
}

// basePath 返回界面挂载路径，统一带结尾斜杠便于拼接子路径与前端相对地址
func (u *UI) basePath() string {
	path := app.NormalizeWebPath(u.opts.Path)
	if path == app.DefaultWebPath {
		return app.DefaultWebPath
	}
	return path + "/"
}

// routes 按角色挂载路由，两个角色不共享任何接口
func (u *UI) routes() *http.ServeMux {
	mux := http.NewServeMux()
	base := u.basePath()
	// 路径前缀不带结尾斜杠时先跳到带斜杠的地址，页面里的相对地址才能正确拼接
	if base != app.DefaultWebPath {
		mux.HandleFunc(strings.TrimSuffix(base, "/"), u.handleBaseRedirect)
	}
	if u.opts.Mode == ModeServer {
		// 服务端展示页与匿名节点接口均无需口令
		mux.HandleFunc(base+"api/nodes", u.handleNodes)
		mux.HandleFunc(base+"api/stream", u.handleStream)
		mux.HandleFunc(base, u.handlePage)
		return mux
	}
	// 客户端页面与接口统一由口令保护
	mux.Handle(base+"api/config", u.guard(http.HandlerFunc(u.handleConfig)))
	mux.Handle(base+"api/log", u.guard(http.HandlerFunc(u.handleLog)))
	mux.Handle(base, u.guard(http.HandlerFunc(u.handlePage)))
	return mux
}

// handleBaseRedirect 把访问路径前缀的请求跳到带结尾斜杠的地址，保留原查询参数
func (u *UI) handleBaseRedirect(w http.ResponseWriter, r *http.Request) {
	target := u.basePath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// Start 在后台监听并开始服务 Web 界面
//
// 返回错误说明界面无法使用，调用方只应记录日志而不必终止主程序
func (u *UI) Start() error {
	listener, err := bind(u.opts)
	if err != nil {
		return err
	}
	u.listener = listener
	u.server = &http.Server{
		Handler:      u.routes(),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}
	server := u.server
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.Logf("webui", "界面服务结束: %v", err)
		}
	}()
	u.logStarted(listener)
	return nil
}

// bind 建立界面监听：配置了套件网关的 unix socket 时只监听它，不占 TCP 端口
func bind(opts Options) (net.Listener, error) {
	if opts.Socket == "" {
		return net.Listen("tcp", net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port)))
	}
	// 上次运行残留的 socket 文件会让监听失败，先清理再重建
	//
	// 监听关闭时系统已自动摘掉该文件，此处的清理只为覆盖被强杀留下的残骸
	if err := os.Remove(opts.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", opts.Socket)
	if err != nil {
		return nil, err
	}
	// 套件网关以套件外用户连接该 socket，只放开读写权限即可
	if err := os.Chmod(opts.Socket, 0o666); err != nil {
		app.Logf("webui", "调整套件 socket 权限失败: %v", err)
	}
	return listener, nil
}

// logStarted 打印界面入口，套件网关接入时入口由网关前缀决定而不是监听地址
func (u *UI) logStarted(listener net.Listener) {
	base := u.basePath()
	entry := "http://" + listener.Addr().String() + base
	if u.opts.Socket != "" {
		entry = "套件网关 socket " + u.opts.Socket + "，挂载路径 " + base
	}
	if u.opts.Mode == ModeServer {
		app.Logf("webui", "节点展示页 %s （只含归属地与流量统计，不含来源 IP 与客户端名称）", entry)
		return
	}
	app.Logf("webui", "配置管理页 %s", entry)
	if u.opts.Token != "" {
		app.Logf("webui", "配置管理页需要口令，首次访问请带上 ?token=<口令> 参数")
	}
}

// Addr 返回实际监听的地址，未启动时为空串
func (u *UI) Addr() string {
	if u.listener == nil {
		return ""
	}
	return u.listener.Addr().String()
}

// Stop 关闭 Web 界面
func (u *UI) Stop() error {
	if u.server == nil {
		return nil
	}
	return u.server.Close()
}

// guard 在配置了口令时校验访问者，并把口令写入 Cookie
func (u *UI) guard(next http.Handler) http.Handler {
	if u.opts.Token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !u.authorized(r) {
			http.Error(w, "配置管理页口令错误", http.StatusUnauthorized)
			return
		}
		if token := r.URL.Query().Get("token"); token != "" {
			http.SetCookie(w, &http.Cookie{
				Name:     tokenCookie,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
			})
		}
		next.ServeHTTP(w, r)
	})
}

// authorized 判断请求是否携带正确口令
func (u *UI) authorized(r *http.Request) bool {
	candidates := []string{r.URL.Query().Get("token"), r.Header.Get("X-TunnelExit-Token")}
	if cookie, err := r.Cookie(tokenCookie); err == nil {
		candidates = append(candidates, cookie.Value)
	}
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		candidates = append(candidates, strings.TrimPrefix(header, "Bearer "))
	}
	for _, candidate := range candidates {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(u.opts.Token)) == 1 {
			return true
		}
	}
	return false
}

// configPath 返回客户端配置文件路径，服务端模式不使用
func (u *UI) configPath() string {
	return filepath.Join(u.opts.AppDir, app.ClientConfigFile)
}
