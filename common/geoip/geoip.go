// geoip 解析 ip2region 的 xdb 离线归属地库，只依赖标准库
//
// 多字节整数一律小端序，IPv4 库的段边界按字节倒序存放，IPv6 库按网络字节序存放
package geoip

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

// xdb 固定布局参数
const (
	headerSize      = 256 // 文件头部长度，保存结构版本与段索引起止指针
	vectorIndexCols = 256 // 向量索引列数，取 IP 的第二个字节
	vectorIndexSize = 8   // 向量索引单项长度，由段索引起止两个 4 字节指针组成
	segmentSizeIPv4 = 14  // IPv4 段索引单项长度，两组 4 字节 IP 加数据长度与指针
	segmentSizeIPv6 = 38  // IPv6 段索引单项长度，两组 16 字节 IP 加数据长度与指针
)

// 支持的结构版本与 IP 版本
const (
	structureIPv4Only      = 2 // 仅覆盖 IPv4 的旧结构，无需判断 IP 版本
	structureWithIPVersion = 3 // 区分 IP 版本的新结构，需读取头部记录的版本号
	ipVersionIPv4          = 4 // 数据覆盖的 IPv4 版本号
	ipVersionIPv6          = 6 // 数据覆盖的 IPv6 版本号
)

// 归属地字段中表示缺失的占位值
const emptyField = "0"

// 归属地字段在数据串中的下标，IPv4 数据串固定为「国家|省份|城市|运营商|国家代码」
const (
	fieldCountry     = 0 // 国家或地区名
	fieldProvince    = 1 // 省份、州或同级行政区
	fieldCity        = 2 // 城市
	fieldISP         = 3 // 运营商
	fieldCountryCode = 4 // 国家代码，用于国家名缺失时兜底
)

// UnknownRegion 是查询不到归属地时的展示文本
const UnknownRegion = "未知地区"

// Region 是一条 IP 归属地记录
type Region struct {
	Country  string // 国家或地区
	Province string // 省份、州或同级行政区
	City     string // 城市
	ISP      string // 运营商
}

// Display 把归属地拼成展示文本，缺失字段跳过，与已有字段重复的值只保留一次
//
// 相邻字段常常同值，例如「北京|北京」，去重后展示文本不会出现连续重复的词
func (r Region) Display() string {
	values := []string{r.Country, r.Province, r.City, r.ISP}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || value == emptyField || contains(parts, value) {
			continue
		}
		parts = append(parts, value)
	}
	if len(parts) == 0 {
		return UnknownRegion
	}
	return strings.Join(parts, " ")
}

// contains 判断展示字段里是否已经出现过同一文本
func contains(parts []string, value string) bool {
	for _, part := range parts {
		if part == value {
			return true
		}
	}
	return false
}

// Database 是一份只读的离线归属地库，只覆盖 IPv4 与 IPv6 中的一个版本
//
// 地址数量有限，查询结果全部缓存，进程重启才会失效
type Database struct {
	path        string                  // 数据文件路径，仅用于错误提示
	file        *os.File                // 打开的数据文件，ReadAt 支持并发读取，关闭后置空
	ipVersion   uint16                  // 这份数据覆盖的 IP 版本，决定地址长度与段边界存放顺序
	addressSize int                     // 地址字节数
	segmentSize int                     // 段索引单项长度
	mu          sync.Mutex              // 保护 cache
	cache       map[string]cachedRegion // 按地址字节缓存查询结果，含未收录的地址
}

// cachedRegion 是带命中标记的缓存项
type cachedRegion struct {
	region Region // 解析出的归属地
	found  bool   // 该地址在库中是否收录
}

// Header 是 xdb 头部里与解析相关的字段
type Header struct {
	Structure uint16 // 文件结构版本
	IPVersion uint16 // 新结构中数据覆盖的 IP 版本
}

// Open 打开一份归属地库文件并校验结构版本
func Open(path string) (*Database, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.Size() <= headerSize {
		_ = file.Close()
		return nil, fmt.Errorf("归属地库过小: %d 字节", info.Size())
	}
	head := make([]byte, headerSize)
	if _, err := file.ReadAt(head, 0); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("读取归属地库头部失败: %w", err)
	}
	header := Header{
		Structure: binary.LittleEndian.Uint16(head),
		IPVersion: binary.LittleEndian.Uint16(head[16:]),
	}
	if header.Structure != structureIPv4Only && header.Structure != structureWithIPVersion {
		_ = file.Close()
		return nil, fmt.Errorf("不支持的归属地库结构版本 %d", header.Structure)
	}
	// 旧结构没有版本号字段，只能按 IPv4 处理
	version := uint16(ipVersionIPv4)
	if header.Structure == structureWithIPVersion {
		version = header.IPVersion
	}
	database := &Database{path: path, file: file, ipVersion: version, cache: make(map[string]cachedRegion)}
	switch version {
	case ipVersionIPv4:
		database.addressSize = net.IPv4len
		database.segmentSize = segmentSizeIPv4
	case ipVersionIPv6:
		database.addressSize = net.IPv6len
		database.segmentSize = segmentSizeIPv6
	default:
		_ = file.Close()
		return nil, fmt.Errorf("归属地库覆盖的 IP 版本为 %d，只支持 IPv4 与 IPv6", version)
	}
	return database, nil
}

// IPVersion 返回这份数据覆盖的 IP 版本
func (d *Database) IPVersion() uint16 {
	if d == nil {
		return 0
	}
	return d.ipVersion
}

// Path 返回数据文件路径
func (d *Database) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// Close 关闭数据文件，重复调用不再产生错误
func (d *Database) Close() error {
	if d == nil || d.file == nil {
		return nil
	}
	err := d.file.Close()
	// 关闭后置空，避免重复关闭报错，也让并发的查询直接按未收录处理
	d.file = nil
	return err
}

// Lookup 查询地址的归属地，地址不属于这份数据覆盖的版本或未收录时返回 false
func (d *Database) Lookup(address string) (Region, bool) {
	if d == nil || d.file == nil {
		return Region{}, false
	}
	parsed := net.ParseIP(address)
	if parsed == nil {
		return Region{}, false
	}
	ip := parsed.To4()
	if d.ipVersion == ipVersionIPv6 {
		// IPv4 地址与 IPv4 映射地址都由 IPv4 库负责，不在这份数据里
		if ip != nil {
			return Region{}, false
		}
		ip = parsed.To16()
	}
	if ip == nil {
		return Region{}, false
	}
	key := string(ip)
	if cached, ok := d.cached(key); ok {
		return cached.region, cached.found
	}
	region, found := d.search(ip)
	d.remember(key, region, found)
	return region, found
}

// cached 读取缓存项
func (d *Database) cached(key string) (cachedRegion, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	item, ok := d.cache[key]
	return item, ok
}

// remember 写入缓存项
func (d *Database) remember(key string, region Region, found bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache[key] = cachedRegion{region: region, found: found}
}

// search 用向量索引定位段索引区间，再二分查找地址所在的段
func (d *Database) search(ip net.IP) (Region, bool) {
	index := make([]byte, vectorIndexSize)
	indexOffset := int64(headerSize + (int(ip[0])*vectorIndexCols+int(ip[1]))*vectorIndexSize)
	if _, err := d.file.ReadAt(index, indexOffset); err != nil {
		return Region{}, false
	}
	start := int64(binary.LittleEndian.Uint32(index))
	end := int64(binary.LittleEndian.Uint32(index[4:]))
	// 起止指针为 0 表示该前缀没有任何数据段
	if start == 0 || end == 0 {
		return Region{}, false
	}
	// 起止指针之差是段个数，可比较的最大下标要减一，否则会读到区间外的记录
	count := int((end - start) / int64(d.segmentSize))
	if count <= 0 {
		return Region{}, false
	}
	segment := make([]byte, d.segmentSize)
	low := 0
	high := count - 1
	for low <= high {
		middle := (low + high) / 2
		if _, err := d.file.ReadAt(segment, start+int64(middle*d.segmentSize)); err != nil {
			return Region{}, false
		}
		if d.compareBounds(ip, segment[0:d.addressSize]) < 0 {
			high = middle - 1
			continue
		}
		if d.compareBounds(ip, segment[d.addressSize:d.addressSize*2]) > 0 {
			low = middle + 1
			continue
		}
		lengthOffset := d.addressSize * 2
		length := int(binary.LittleEndian.Uint16(segment[lengthOffset : lengthOffset+2]))
		if length == 0 {
			return Region{}, false
		}
		pointer := int64(binary.LittleEndian.Uint32(segment[lengthOffset+2 : lengthOffset+6]))
		data := make([]byte, length)
		if _, err := d.file.ReadAt(data, pointer); err != nil {
			return Region{}, false
		}
		return Parse(string(data)), true
	}
	return Region{}, false
}

// compareBounds 把网络字节序的地址与段边界按数值比较
//
// IPv4 库的段边界按字节倒序存放，IPv6 库按网络字节序存放，因此取值顺序不同
func (d *Database) compareBounds(ip net.IP, stored []byte) int {
	for i := 0; i < len(ip); i++ {
		left := ip[i]
		right := stored[i]
		if d.ipVersion == ipVersionIPv4 {
			right = stored[len(ip)-1-i]
		}
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
	}
	return 0
}

// Library 是归属地库集合，按地址族分别持有一份 IPv4 与 IPv6 数据文件
//
// 每个 IP 版本最多装载一份，未装载的版本一律按未收录处理，
// 因此某一份库文件缺失或损坏只影响对应地址族的展示
type Library struct {
	// v4 是 IPv4 库，未装载时为 nil
	v4 *Database
	// v6 是 IPv6 库，未装载时为 nil
	v6 *Database
}

// NewLibrary 创建尚未装载数据文件的归属地库集合
func NewLibrary() *Library {
	return &Library{}
}

// Load 打开一份数据文件并按它覆盖的 IP 版本归档，同一版本重复装载时报错
//
// 重复装载只提示不覆盖，避免把先装载的库换掉后两个版本落进同一份数据
func (l *Library) Load(path string) error {
	database, err := Open(path)
	if err != nil {
		return err
	}
	if database.ipVersion == ipVersionIPv6 {
		if l.v6 != nil {
			_ = database.Close()
			return fmt.Errorf("已装载 IPv6 归属地库 %s", l.v6.Path())
		}
		l.v6 = database
		return nil
	}
	if l.v4 != nil {
		_ = database.Close()
		return fmt.Errorf("已装载 IPv4 归属地库 %s", l.v4.Path())
	}
	l.v4 = database
	return nil
}

// Lookup 按地址族选用对应的库查询，未装载相应库时返回未收录
func (l *Library) Lookup(address string) (Region, bool) {
	if l == nil {
		return Region{}, false
	}
	parsed := net.ParseIP(address)
	if parsed == nil {
		return Region{}, false
	}
	if parsed.To4() != nil {
		return l.v4.Lookup(address)
	}
	return l.v6.Lookup(address)
}

// Paths 返回已装载的数据文件路径，IPv4 在前
func (l *Library) Paths() []string {
	if l == nil {
		return nil
	}
	paths := make([]string, 0, 2)
	if l.v4 != nil {
		paths = append(paths, l.v4.Path())
	}
	if l.v6 != nil {
		paths = append(paths, l.v6.Path())
	}
	return paths
}

// Close 关闭全部已装载的数据文件
func (l *Library) Close() error {
	if l == nil {
		return nil
	}
	v4Err := l.v4.Close()
	v6Err := l.v6.Close()
	l.v4 = nil
	l.v6 = nil
	if v4Err != nil {
		return v4Err
	}
	return v6Err
}

// Parse 把「国家|省份|城市|运营商|国家代码」拆成结构化归属地
func Parse(raw string) Region {
	fields := strings.Split(raw, "|")
	field := func(index int) string {
		if index >= len(fields) {
			return ""
		}
		value := strings.TrimSpace(fields[index])
		if value == emptyField {
			return ""
		}
		return value
	}
	region := Region{
		Country:  field(fieldCountry),
		Province: field(fieldProvince),
		City:     field(fieldCity),
		ISP:      field(fieldISP),
	}
	if region.Country == "" {
		region.Country = field(fieldCountryCode)
	}
	return region
}
