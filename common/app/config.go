// config 负责定位程序目录并读写隧道服务端与客户端配置
package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 配置文件与运行时数据文件名，均位于程序目录
const (
	ServerConfigFile   = "server.json"
	ClientConfigFile   = "client.json"
	ClientsStoreFile   = "clients.json"
	DefaultLogDir      = "logs"             // 日志子目录
	DefaultLogFile     = "tunnelexit.log"   // 程序日志文件名
	DefaultGeoIPFile   = "ip2region_v4.xdb" // IPv4 离线归属地库
	DefaultGeoIPFileV6 = "ip2region_v6.xdb" // IPv6 离线归属地库
	clientIDBytes      = 8                  // 客户端唯一 ID 的随机字节数
)

// 默认监听与端口分配参数
const (
	DefaultTunnelPort     = 9733 // 隧道端口，登录与预建空闲连接共用
	DefaultSocksHost      = "0.0.0.0"
	DefaultSocksPortBase  = 19733 // SOCKS5 端口范围起点，与服务端自身监听端口错开
	DefaultSocksPortCount = 2500  // SOCKS5 端口范围容量，按客户端逐一固定分配
	DefaultPoolSize       = 16
	MaxPoolSize           = 256
	MaxSocksPort          = 65535
	DefaultWebHost        = "127.0.0.1" // 界面默认只对本机开放
	DefaultServerWebPort  = 9732        // 服务端展示页端口
	DefaultClientWebPort  = 7003        // 客户端配置页端口，与前者错开以便同机运行
	DefaultWebPath        = "/"         // 界面挂载路径，根路径表示挂在监听端口根上
)

// 日志归档与保留参数：历史日志按天归档，早于保留天数的归档直接删除，保留天数为零表示不写日志文件
const (
	DefaultLogKeepDays = 3   // 日志文件保留天数
	minLogKeepDays     = 0   // 保留天数的下限，零表示不写日志文件
	maxLogKeepDays     = 365 // 保留天数的上限，避免误配后永不清理
)

// 节点记录保留参数，零表示不删除
const (
	DefaultNodeKeepDays = 0   // 节点记录保留天数
	maxNodeKeepDays     = 365 // 保留天数的上限
)

// ServerConfig 是服务端运行参数，端口分配结果另存 clients.json
type ServerConfig struct {
	Port           int    `json:"port"`           // 隧道端口，客户端登录与预建空闲连接都接入该端口
	SocksHost      string `json:"socksHost"`      // SOCKS5 端口监听地址
	SocksPortBase  int    `json:"socksPortBase"`  // 客户端 SOCKS5 端口范围的起始端口
	SocksPortCount int    `json:"socksPortCount"` // SOCKS5 端口范围容量，用满后拒绝新客户端
	PoolSize       int    `json:"poolSize"`       // 每个客户端要求维持的空闲连接数
	NodeKeepDays   int    `json:"nodeKeepDays"`   // 节点记录保留天数，超过该天数未在线的记录会被清理
	WebHost        string `json:"webHost"`        // 节点展示页监听地址，改成 0.0.0.0 后可从其他机器打开
	WebPort        int    `json:"webPort"`        // 节点展示页监听端口
	WebPath        string `json:"webPath"`        // 节点展示页挂载的路径前缀，经反向代理走子路径时填写
}

// ClientConfig 是客户端连接服务端所需参数
type ClientConfig struct {
	ServerHost string `json:"serverHost"` // 隧道服务端地址
	Port       int    `json:"port"`       // 隧道服务端端口，登录与预建空闲连接共用
	ServerKey  string `json:"serverKey"`  // 服务端公钥 PEM 文本，客户端据此校验对端身份，未填写或填错一律无法连接
	ID         string `json:"id"`         // 客户端唯一标识，首次运行生成后固定不变
	Name       string `json:"name"`       // 客户端显示名，只写进服务端日志与 clients.json
	PoolSize   int    `json:"poolSize"`   // 客户端维持的空闲连接数，为 0 时采用服务端下发值
	WebHost    string `json:"webHost"`    // 配置管理页监听地址，改成 0.0.0.0 后可从其他机器打开
	WebPort    int    `json:"webPort"`    // 配置管理页监听端口
	WebPath    string `json:"webPath"`    // 配置管理页挂载的路径前缀，经反向代理走子路径时填写
}

// AppDir 返回程序运行目录，可用 TUNNELEXIT_APP_DIR 覆盖
func AppDir() string {
	if dir := strings.TrimSpace(os.Getenv("TUNNELEXIT_APP_DIR")); dir != "" {
		return dir
	}
	exe, err := os.Executable()
	if err != nil {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return "."
		}
		return wd
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// GeoIPPath 返回 IPv4 离线归属地库路径，可用 TUNNELEXIT_GEOIP_FILE 覆盖
//
// 指向不存在的文件等于关闭归属地展示，服务端只记日志不退出
func GeoIPPath(appDir string) string {
	if path := strings.TrimSpace(os.Getenv("TUNNELEXIT_GEOIP_FILE")); path != "" {
		return path
	}
	return filepath.Join(appDir, DefaultGeoIPFile)
}

// GeoIPPathV6 返回 IPv6 离线归属地库路径，覆盖方式与 IPv4 库一致
func GeoIPPathV6(appDir string) string {
	if path := strings.TrimSpace(os.Getenv("TUNNELEXIT_GEOIP_FILE_V6")); path != "" {
		return path
	}
	return filepath.Join(appDir, DefaultGeoIPFileV6)
}

// Platform 返回打包平台标识，开发运行时为 none
func Platform() string {
	value := strings.TrimSpace(os.Getenv("PACK_PLATFORM"))
	if value == "" {
		return "none"
	}
	return value
}

// DefaultServerConfig 返回服务端默认配置
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Port:           DefaultTunnelPort,
		SocksHost:      DefaultSocksHost,
		SocksPortBase:  DefaultSocksPortBase,
		SocksPortCount: DefaultSocksPortCount,
		PoolSize:       DefaultPoolSize,
		NodeKeepDays:   DefaultNodeKeepDays,
		WebHost:        DefaultWebHost,
		WebPort:        DefaultServerWebPort,
		WebPath:        DefaultWebPath,
	}
}

// LoadServerConfig 读取服务端配置，缺失字段回落到默认值
func LoadServerConfig(path string) ServerConfig {
	cfg := DefaultServerConfig()
	if _, err := ReadJSON(path, &cfg); err != nil {
		Logf("config", "读取服务端配置失败，使用默认值: %v", err)
	}
	applyServerEnv(&cfg)
	normalizeServerConfig(&cfg)
	return cfg
}

// SaveServerConfig 原子写入服务端配置
func SaveServerConfig(path string, cfg ServerConfig) error {
	normalizeServerConfig(&cfg)
	return WriteJSONAtomic(path, cfg)
}

// applyServerEnv 应用服务端相关的环境变量覆盖，便于容器首次启动免配置
func applyServerEnv(cfg *ServerConfig) {
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_PORT")); value != "" {
		if port, err := strconv.Atoi(value); err == nil {
			cfg.Port = port
		}
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_SOCKS_HOST")); value != "" {
		cfg.SocksHost = value
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_SOCKS_PORT_BASE")); value != "" {
		if port, err := strconv.Atoi(value); err == nil {
			cfg.SocksPortBase = port
		}
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_SOCKS_PORT_COUNT")); value != "" {
		if count, err := strconv.Atoi(value); err == nil {
			cfg.SocksPortCount = count
		}
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_POOL_SIZE")); value != "" {
		if size, err := strconv.Atoi(value); err == nil {
			cfg.PoolSize = size
		}
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_NODE_KEEP_DAYS")); value != "" {
		if days, err := strconv.Atoi(value); err == nil {
			cfg.NodeKeepDays = days
		}
	}
	applyWebEnv(&cfg.WebHost, &cfg.WebPort, &cfg.WebPath)
}

// applyWebEnv 应用两个角色界面共用的监听地址、端口与路径前缀覆盖
func applyWebEnv(host *string, port *int, path *string) {
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_WEB_HOST")); value != "" {
		*host = value
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_WEB_PORT")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			*port = parsed
		}
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_WEB_PATH")); value != "" {
		*path = value
	}
}

// WebToken 返回客户端配置管理页的访问口令，为空表示不做校验
func WebToken() string {
	return strings.TrimSpace(os.Getenv("TUNNELEXIT_WEB_TOKEN"))
}

// WebSocket 返回套件网关接入的 unix socket 路径
//
// 路径由平台启动脚本下发，为空时界面按 webHost 与 webPort 走 TCP
func WebSocket() string {
	return strings.TrimSpace(os.Getenv("TUNNELEXIT_WEB_SOCKET"))
}

// LogPath 返回运行目录下程序日志的文件路径
func LogPath(appDir string) string {
	return filepath.Join(appDir, DefaultLogDir, DefaultLogFile)
}

// LogKeepDays 返回日志文件保留天数，TUNNELEXIT_LOG_KEEP_DAYS 可覆盖，零表示不写日志文件
func LogKeepDays() int {
	value := strings.TrimSpace(os.Getenv("TUNNELEXIT_LOG_KEEP_DAYS"))
	if value == "" {
		return DefaultLogKeepDays
	}
	days, err := strconv.Atoi(value)
	if err != nil {
		return DefaultLogKeepDays
	}
	return normalizeLogKeepDays(days)
}

// normalizeLogKeepDays 把日志保留天数收敛到允许区间
func normalizeLogKeepDays(days int) int {
	if days < minLogKeepDays {
		return minLogKeepDays
	}
	if days > maxLogKeepDays {
		return maxLogKeepDays
	}
	return days
}

// normalizeNodeKeepDays 把节点记录保留天数收敛到允许区间
func normalizeNodeKeepDays(days int) int {
	if days <= DefaultNodeKeepDays {
		return DefaultNodeKeepDays
	}
	if days > maxNodeKeepDays {
		return maxNodeKeepDays
	}
	return days
}

// normalizeServerConfig 修正非法端口与端口池大小，保证监听参数可用
func normalizeServerConfig(cfg *ServerConfig) {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = DefaultTunnelPort
	}
	if strings.TrimSpace(cfg.SocksHost) == "" {
		cfg.SocksHost = DefaultSocksHost
	}
	if cfg.SocksPortBase <= 0 || cfg.SocksPortBase > MaxSocksPort {
		cfg.SocksPortBase = DefaultSocksPortBase
	}
	if cfg.SocksPortCount <= 0 {
		cfg.SocksPortCount = DefaultSocksPortCount
	}
	// 端口范围不得越过可用端口上限，否则分配时会落到无效端口
	if limit := MaxSocksPort - cfg.SocksPortBase + 1; cfg.SocksPortCount > limit {
		cfg.SocksPortCount = limit
	}
	cfg.PoolSize = NormalizePoolSize(cfg.PoolSize)
	cfg.NodeKeepDays = normalizeNodeKeepDays(cfg.NodeKeepDays)
	normalizeWebField(&cfg.WebHost, &cfg.WebPort, DefaultServerWebPort)
	cfg.WebPath = NormalizeWebPath(cfg.WebPath)
}

// NormalizeWebPath 规范化界面挂载路径：空值表示根路径，统一去掉结尾斜杠便于拼接
func NormalizeWebPath(value string) string {
	path := strings.TrimSpace(value)
	if path == "" || path == "/" {
		return DefaultWebPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path = strings.TrimRight(path, "/"); path == "" {
		return DefaultWebPath
	}
	return path
}

// normalizeWebField 修正界面的监听地址与端口，不检查是否与隧道端口重合
func normalizeWebField(host *string, port *int, fallback int) {
	if strings.TrimSpace(*host) == "" {
		*host = DefaultWebHost
	}
	if *port <= 0 || *port > 65535 {
		*port = fallback
	}
}

// NormalizePoolSize 把连接池大小收敛到允许区间
func NormalizePoolSize(size int) int {
	if size <= 0 {
		return DefaultPoolSize
	}
	if size > MaxPoolSize {
		return MaxPoolSize
	}
	return size
}

// DefaultClientConfig 返回客户端默认配置
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		ServerHost: "127.0.0.1",
		Port:       DefaultTunnelPort,
		ID:         NewClientID(),
		Name:       defaultClientName(),
		PoolSize:   DefaultPoolSize,
		WebHost:    DefaultWebHost,
		WebPort:    DefaultClientWebPort,
		WebPath:    DefaultWebPath,
	}
}

// defaultClientName 用主机名生成默认客户端名，便于在服务端日志里识别来源
func defaultClientName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "client"
	}
	return host
}

// LoadClientConfig 读取客户端配置，缺失字段回落到默认值
func LoadClientConfig(path string) ClientConfig {
	cfg := DefaultClientConfig()
	if _, err := ReadJSON(path, &cfg); err != nil {
		Logf("config", "读取客户端配置失败，使用默认值: %v", err)
	}
	applyClientEnv(&cfg)
	normalizeClientConfig(&cfg)
	return cfg
}

// SaveClientConfig 原子写入客户端配置
func SaveClientConfig(path string, cfg ClientConfig) error {
	normalizeClientConfig(&cfg)
	return WriteJSONAtomic(path, cfg)
}

// NewClientID 生成客户端唯一 ID，服务端据此结合来源 IP 分配固定的 SOCKS5 端口
//
// 用随机字节而不是主机信息，同机运行多个客户端才能各自拿到独立端口
func NewClientID() string {
	buf := make([]byte, clientIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%0*x", clientIDBytes*2, time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// EnsureClientID 保证客户端 ID 已落盘，避免每次启动生成新 ID 导致端口变化
func EnsureClientID(path string, cfg ClientConfig) ClientConfig {
	if storedClientID(path) == cfg.ID {
		return cfg
	}
	if err := SaveClientConfig(path, cfg); err != nil {
		Logf("config", "保存客户端 ID 失败，本次仍按内存值运行: %v", err)
	}
	return cfg
}

// storedClientID 读取配置文件里已保存的客户端 ID，文件缺失或损坏时返回空值
func storedClientID(path string) string {
	var record struct {
		ID string `json:"id"`
	}
	if _, err := ReadJSON(path, &record); err != nil {
		return ""
	}
	return strings.TrimSpace(record.ID)
}

// applyClientEnv 应用客户端相关的环境变量覆盖
func applyClientEnv(cfg *ClientConfig) {
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_SERVER")); value != "" {
		cfg.ServerHost = value
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_PORT")); value != "" {
		if port, err := strconv.Atoi(value); err == nil {
			cfg.Port = port
		}
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_SERVER_KEY")); value != "" {
		cfg.ServerKey = value
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_CLIENT_ID")); value != "" {
		cfg.ID = value
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_CLIENT_NAME")); value != "" {
		cfg.Name = value
	}
	if value := strings.TrimSpace(os.Getenv("TUNNELEXIT_CLIENT_POOL_SIZE")); value != "" {
		if size, err := strconv.Atoi(value); err == nil {
			cfg.PoolSize = size
		}
	}
	applyWebEnv(&cfg.WebHost, &cfg.WebPort, &cfg.WebPath)
}

// normalizeClientConfig 修正非法地址与端口，保证连接参数可用
func normalizeClientConfig(cfg *ClientConfig) {
	if strings.TrimSpace(cfg.ServerHost) == "" {
		cfg.ServerHost = "127.0.0.1"
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = DefaultTunnelPort
	}
	// 公钥只做首尾空白清理，格式校验交给连接阶段，便于配置页仍能打开并让用户改回来
	cfg.ServerKey = strings.TrimSpace(cfg.ServerKey)
	if strings.TrimSpace(cfg.ID) == "" {
		cfg.ID = NewClientID()
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = defaultClientName()
	}
	cfg.PoolSize = NormalizePoolSize(cfg.PoolSize)
	normalizeWebField(&cfg.WebHost, &cfg.WebPort, DefaultClientWebPort)
	cfg.WebPath = NormalizeWebPath(cfg.WebPath)
}
