// nodes 在服务端内部维护客户端到 SOCKS5 端口、流量统计的持久化记录
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sync"
	"time"

	"tunnelexit/app"
)

// NodeRecord 是按客户端唯一 ID 保存的持久化记录
type NodeRecord struct {
	Address  string    `json:"address"`  // 最近一次登录的来源 IP，与客户端 ID 共同确定端口起点
	Port     int       `json:"port"`     // 分配给该客户端的 SOCKS5 端口
	Name     string    `json:"name"`     // 客户端上报的显示名
	Up       uint64    `json:"up"`       // 累计上行字节数，外部客户端发往目标方向
	Down     uint64    `json:"down"`     // 累计下行字节数，目标返回给外部客户端方向
	LastSeen time.Time `json:"lastSeen"` // 最近一次确认在线的时间
}

// NodeStore 管理客户端到 SOCKS5 端口与流量统计的持久化记录
//
// 端口按客户端 ID 与来源 IP 的组合分配：同一个 ID 在原地址重连始终占用同一个端口，
// 同一台机器上运行多个客户端也能各自拿到独立端口
type NodeStore struct {
	mu      sync.Mutex             // 保护 records、dirty 与分配过程
	path    string                 // 记录文件路径
	records map[string]*NodeRecord // 按客户端唯一 ID 保存记录
	dirty   bool                   // 内存记录与磁盘不一致，需要落盘
}

// NewNodeStore 创建节点记录存储
func NewNodeStore(path string) *NodeStore {
	return &NodeStore{path: path, records: make(map[string]*NodeRecord)}
}

// Load 从磁盘读取节点记录，文件缺失时按空记录处理
//
// 记录一律照文件内容装载，端口与来源地址只在分配阶段按当前配置校正
func (n *NodeStore) Load() error {
	data, err := os.ReadFile(n.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	loaded := make(map[string]NodeRecord)
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for clientID, record := range loaded {
		stored := record
		n.records[clientID] = &stored
	}
	return nil
}

// Record 返回指定客户端的记录副本
func (n *NodeStore) Record(clientID string) (NodeRecord, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	record, ok := n.records[clientID]
	if !ok {
		return NodeRecord{}, false
	}
	return *record, true
}

// PortSeed 用客户端 ID 与来源 IP 算出端口分配起点在范围内的偏移
//
// 同一对组合始终得到同一个偏移，因此重连与服务端重启后都落回同一个端口
func PortSeed(clientID, address string, count int) int {
	if count <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(clientID))
	_, _ = hash.Write([]byte("|"))
	_, _ = hash.Write([]byte(address))
	return int(hash.Sum64() % uint64(count))
}

// Reserve 为客户端分配 SOCKS5 端口，端口只落在 [base, base+count-1] 内
//
// 已分配且仍可用的记录直接复用，否则从该客户端的专属起点起顺序挑选，范围用满时由调用方拒绝登录
func (n *NodeStore) Reserve(clientID, address, name string, base, count int, inUse func(int) bool) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	record := n.records[clientID]
	if record != nil && record.Port >= base && record.Port < base+count && !inUse(record.Port) {
		n.renameLocked(record, name)
		n.addressLocked(record, address)
		return record.Port, nil
	}
	port := n.pickFreeLocked(clientID, address, base, count, inUse)
	if port == 0 {
		return 0, fmt.Errorf("SOCKS5 端口范围 %d-%d 已用满，请调大 socksPortCount", base, base+count-1)
	}
	if record == nil {
		record = &NodeRecord{}
		n.records[clientID] = record
	}
	if record.Port != port {
		record.Port = port
		n.dirty = true
	}
	n.renameLocked(record, name)
	n.addressLocked(record, address)
	return port, nil
}

// renameLocked 记录客户端显示名，名称变化时标记待落盘
func (n *NodeStore) renameLocked(record *NodeRecord, name string) {
	if name == "" || record.Name == name {
		return
	}
	record.Name = name
	n.dirty = true
}

// addressLocked 记录客户端来源 IP，地址变化时标记待落盘，只在旧会话已退出后调用
func (n *NodeStore) addressLocked(record *NodeRecord, address string) {
	if address == "" || record.Address == address {
		return
	}
	record.Address = address
	n.dirty = true
}

// pickFreeLocked 从客户端专属起点开始顺序挑选空闲端口，超出范围时回头从头继续
func (n *NodeStore) pickFreeLocked(clientID, address string, base, count int, inUse func(int) bool) int {
	used := make(map[int]bool, len(n.records))
	for other, record := range n.records {
		if other != clientID {
			used[record.Port] = true
		}
	}
	start := PortSeed(clientID, address, count)
	for offset := 0; offset < count; offset++ {
		port := base + (start+offset)%count
		if used[port] || inUse(port) {
			continue
		}
		return port
	}
	return 0
}

// Touch 更新客户端的最近在线时间，没有记录时忽略
func (n *NodeStore) Touch(clientID string, at time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	record, ok := n.records[clientID]
	if !ok || !at.After(record.LastSeen) {
		return
	}
	record.LastSeen = at
	n.dirty = true
}

// Prune 删除超过保留天数未在线的节点记录，保留天数不大于零时不删除
func (n *NodeStore) Prune(keepDays int, now time.Time) int {
	if keepDays <= 0 {
		return 0
	}
	deadline := now.AddDate(0, 0, -keepDays)
	n.mu.Lock()
	defer n.mu.Unlock()
	removed := 0
	for clientID, record := range n.records {
		if !record.LastSeen.Before(deadline) {
			continue
		}
		delete(n.records, clientID)
		removed++
	}
	if removed > 0 {
		n.dirty = true
	}
	return removed
}

// AddTraffic 累加客户端的双向流量，没有记录时忽略
func (n *NodeStore) AddTraffic(clientID string, up, down uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	record, ok := n.records[clientID]
	if !ok {
		return
	}
	record.Up += up
	record.Down += down
	n.dirty = true
}

// Remove 删除客户端的记录与流量统计
func (n *NodeStore) Remove(clientID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.records[clientID]; !ok {
		return
	}
	delete(n.records, clientID)
	n.dirty = true
}

// Snapshot 返回按客户端唯一 ID 索引的记录快照
func (n *NodeStore) Snapshot() map[string]NodeRecord {
	n.mu.Lock()
	defer n.mu.Unlock()
	snapshot := make(map[string]NodeRecord, len(n.records))
	for clientID, record := range n.records {
		snapshot[clientID] = *record
	}
	return snapshot
}

// Save 原子写入节点记录，没有变化时不落盘
func (n *NodeStore) Save() error {
	n.mu.Lock()
	if !n.dirty {
		n.mu.Unlock()
		return nil
	}
	snapshot := make(map[string]NodeRecord, len(n.records))
	for clientID, record := range n.records {
		snapshot[clientID] = *record
	}
	n.dirty = false
	n.mu.Unlock()
	dir := filepath.Dir(n.path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			n.markDirty()
			return err
		}
	}
	if err := app.WriteJSONAtomic(n.path, snapshot); err != nil {
		// 落盘失败时恢复待落盘标记，交由下一次保存重试
		n.markDirty()
		return err
	}
	return nil
}

// markDirty 标记记录待落盘
func (n *NodeStore) markDirty() {
	n.mu.Lock()
	n.dirty = true
	n.mu.Unlock()
}
