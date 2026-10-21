// nodes_test 验证服务端按客户端唯一 ID 与来源 IP 分配端口、统计流量、清理过期记录与持久化恢复
package test

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tunnelexit/app"
	"tunnelexit/server"
)

// noPortInUse 表示所有候选端口都空闲
func noPortInUse(int) bool {
	return false
}

// nodeID 由来源地址派生出稳定的客户端 ID，便于同一地址在多次调用中保持同一端口
func nodeID(address string) string {
	return "id-" + address
}

// reserveNode 为来源地址分配端口，失败时直接结束测试
func reserveNode(t *testing.T, store *server.NodeStore, address, name string, base, count int) int {
	t.Helper()
	port, err := store.Reserve(nodeID(address), address, name, base, count, noPortInUse)
	if err != nil {
		t.Fatalf("为 %s 分配端口失败: %v", address, err)
	}
	return port
}

// TestNodeStoreAssignsDistinctPorts 验证同一来源地址上不同 ID 各自拿到范围内的独立端口
func TestNodeStoreAssignsDistinctPorts(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	const count = 4
	base := 20580
	clientIDs := []string{"alpha", "beta", "gamma", "delta"}
	seen := make(map[int]string)
	for _, clientID := range clientIDs {
		port, err := store.Reserve(clientID, "10.0.0.1", "node", base, count, noPortInUse)
		if err != nil {
			t.Fatalf("为 %s 分配端口失败: %v", clientID, err)
		}
		if port < base || port >= base+count {
			t.Fatalf("端口应落在 %d-%d 内，实际 %d", base, base+count-1, port)
		}
		if other, ok := seen[port]; ok {
			t.Fatalf("端口 %d 被 %s 与 %s 重复占用", port, other, clientID)
		}
		seen[port] = clientID
	}
}

// TestNodeStoreIsStable 验证同一客户端重复分配总是得到同一个端口
func TestNodeStoreIsStable(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	first := reserveNode(t, store, "10.0.0.9", "stable", 21080, 10)
	for i := 0; i < 5; i++ {
		again := reserveNode(t, store, "10.0.0.9", "stable", 21080, 10)
		if again != first {
			t.Fatalf("端口不稳定: 首次 %d，第 %d 次 %d", first, i, again)
		}
	}
}

// TestNodeStoreSkipsUsedPorts 验证起点端口被占用时按顺序顺延
func TestNodeStoreSkipsUsedPorts(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	base := 22080
	address := "10.0.0.4"
	const count = 10
	start := base + server.PortSeed(nodeID(address), address, count)
	busy := map[int]bool{start: true}
	inUse := func(port int) bool { return busy[port] }
	port, err := store.Reserve(nodeID(address), address, "node", base, count, inUse)
	if err != nil {
		t.Fatalf("分配失败: %v", err)
	}
	merged := base + (start-base+1)%count
	if port != merged {
		t.Fatalf("应顺延到 %d，实际 %d", merged, port)
	}
}

// TestNodeStoreReallocatesWhenPortBusy 验证记录端口被外部占用时才会重新分配
func TestNodeStoreReallocatesWhenPortBusy(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	base := 23080
	first := reserveNode(t, store, "10.0.0.5", "keep", base, 10)
	second := reserveNode(t, store, "10.0.0.5", "keep", base, 10)
	if second != first {
		t.Fatalf("端口空闲时应复用 %d，实际 %d", first, second)
	}
	third, err := store.Reserve(nodeID("10.0.0.5"), "10.0.0.5", "keep", base, 10, func(port int) bool { return port == first })
	if err != nil {
		t.Fatalf("重新分配失败: %v", err)
	}
	if third == first {
		t.Fatalf("端口被占用时不应继续返回 %d", first)
	}
}

// TestNodeStoreRangeExhausted 验证端口范围用满后拒绝新的客户端
func TestNodeStoreRangeExhausted(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	base := 26080
	first := reserveNode(t, store, "10.0.1.1", "a", base, 2)
	reserveNode(t, store, "10.0.1.2", "b", base, 2)
	if _, err := store.Reserve(nodeID("10.0.1.3"), "10.0.1.3", "c", base, 2, noPortInUse); err == nil {
		t.Fatal("范围用满时应拒绝新的客户端")
	}
	// 已在范围内的客户端仍可继续复用原端口
	if port := reserveNode(t, store, "10.0.1.1", "a", base, 2); port != first {
		t.Fatalf("已分配客户端应复用端口 %d，实际 %d", first, port)
	}
}

// TestNodeStorePrunesStaleRecords 验证清理只删除超过保留天数未在线的记录
func TestNodeStorePrunesStaleRecords(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	now := time.Now()
	reserveNode(t, store, "10.0.2.1", "stale", 24080, 10)
	reserveNode(t, store, "10.0.2.2", "fresh", 24080, 10)
	store.Touch("id-10.0.2.1", now.AddDate(0, 0, -10))
	store.Touch("id-10.0.2.2", now.AddDate(0, 0, -1))

	if removed := store.Prune(0, now); removed != 0 {
		t.Fatalf("保留天数为零时不应删除记录，实际删除 %d 条", removed)
	}
	if got := len(store.Snapshot()); got != 2 {
		t.Fatalf("保留天数为零时记录数 %d，期望 2", got)
	}
	if removed := store.Prune(7, now); removed != 1 {
		t.Fatalf("超过 7 天未在线的记录应删除 1 条，实际 %d 条", removed)
	}
	snapshot := store.Snapshot()
	if _, ok := snapshot["id-10.0.2.1"]; ok {
		t.Fatal("超过保留天数未在线的记录未被清理")
	}
	if _, ok := snapshot["id-10.0.2.2"]; !ok {
		t.Fatal("保留天数内的记录被误删")
	}
}

// TestServerConfigNodeKeepDays 验证节点记录保留天数可由环境变量覆盖
func TestServerConfigNodeKeepDays(t *testing.T) {
	clearTunnelEnv(t)
	path := filepath.Join(t.TempDir(), app.ServerConfigFile)
	cases := []struct {
		value string
		want  int
	}{
		{"", app.DefaultNodeKeepDays},
		{"7", 7},
		{"0", app.DefaultNodeKeepDays},
		{"-3", app.DefaultNodeKeepDays},
		{"abc", app.DefaultNodeKeepDays},
	}
	for _, item := range cases {
		t.Setenv("TUNNELEXIT_NODE_KEEP_DAYS", item.value)
		if got := app.LoadServerConfig(path).NodeKeepDays; got != item.want {
			t.Fatalf("环境变量为 %q 时保留天数 %d，期望 %d", item.value, got, item.want)
		}
	}
}

// TestNodeStoreShrinksToSinglePort 验证范围缩小到只剩一个端口时旧端口会被改写、多余客户端被拒绝
func TestNodeStoreShrinksToSinglePort(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	base := 27080
	first := reserveNode(t, store, "10.0.2.1", "a", base, 10)
	// 原端口可能已被收缩到范围外，只保留基准端口时必然回到基准端口
	if got := reserveNode(t, store, "10.0.2.1", "a", base, 1); got != base {
		t.Fatalf("范围收缩后应回到基准端口 %d，原端口 %d 实际 %d", base, first, got)
	}
	if _, err := store.Reserve(nodeID("10.0.2.2"), "10.0.2.2", "b", base, 1, noPortInUse); err == nil {
		t.Fatal("范围只剩一个端口时应拒绝新的客户端")
	}
}

// TestNodeStoreTrafficAndTouch 验证流量累加与最近在线时间只前进不回退
func TestNodeStoreTrafficAndTouch(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	address := "10.0.3.1"
	clientID := nodeID(address)
	reserveNode(t, store, address, "traffic", 28080, 10)
	store.AddTraffic(clientID, 1024, 2048)
	store.AddTraffic(clientID, 1, 2)
	base := time.Now().Add(-time.Minute)
	store.Touch(clientID, base)
	store.Touch(clientID, base.Add(-time.Hour))
	record, ok := store.Record(clientID)
	if !ok {
		t.Fatal("记录应存在")
	}
	if record.Up != 1025 || record.Down != 2050 {
		t.Fatalf("流量应累加为 1025/2050，实际 %d/%d", record.Up, record.Down)
	}
	if !record.LastSeen.Equal(base) {
		t.Fatalf("最近在线时间不应回退，实际 %s", record.LastSeen)
	}
}

// TestNodeStorePersistence 验证端口、名称与流量能落盘并在新实例中恢复
func TestNodeStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	first := server.NewNodeStore(path)
	address := "10.0.4.1"
	clientID := nodeID(address)
	port := reserveNode(t, first, address, "persist", 29080, 10)
	first.AddTraffic(clientID, 10, 20)
	first.Touch(clientID, time.Now())
	if err := first.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	second := server.NewNodeStore(path)
	if err := second.Load(); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	record, ok := second.Record(clientID)
	if !ok {
		t.Fatal("恢复后应能查到记录")
	}
	if record.Port != port || record.Name != "persist" || record.Up != 10 || record.Down != 20 {
		t.Fatalf("恢复结果不符: %+v", record)
	}
	if record.Address != address {
		t.Fatalf("恢复后应保留来源 IP，实际 %s", record.Address)
	}
	if got := reserveNode(t, second, address, "persist", 29080, 10); got != port {
		t.Fatalf("恢复后应复用端口 %d，实际 %d", port, got)
	}
}

// TestNodeStoreSaveWithoutChange 验证没有变化时不落盘
func TestNodeStoreSaveWithoutChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	store := server.NewNodeStore(path)
	if err := store.Save(); err != nil {
		t.Fatalf("空存储保存不应报错: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("没有变化时不应生成记录文件")
	}
}

// TestNodeStoreLoadAsWritten 验证记录一律按文件内容装载，端口只在分配阶段校正
func TestNodeStoreLoadAsWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	content := `{"id-ok":{"address":"10.0.5.1","port":30080},"id-no-address":{"port":30081},"id-no-port":{"address":"10.0.5.3","port":0},"id-bad-port":{"address":"10.0.5.4","port":99999}}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	store := server.NewNodeStore(path)
	if err := store.Load(); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	record, ok := store.Record("id-ok")
	if !ok || record.Port != 30080 || record.Address != "10.0.5.1" {
		t.Fatalf("记录应按文件内容装载，实际 %+v/%v", record, ok)
	}
	record, ok = store.Record("id-bad-port")
	if !ok || record.Port != 99999 {
		t.Fatalf("超范围端口的记录也应照原值装载，实际 %+v/%v", record, ok)
	}
	// 超范围端口不会发出去，分配时按当前配置重新挑选
	base := 41000
	port, err := store.Reserve("id-bad-port", "10.0.5.4", "", base, 4, func(int) bool { return false })
	if err != nil {
		t.Fatalf("重新分配端口失败: %v", err)
	}
	if port < base || port >= base+4 {
		t.Fatalf("重新分配的端口 %d 应落在 %d-%d 内", port, base, base+3)
	}
}

// TestNodeStoreLoadMissingFile 验证记录文件缺失时按空处理
func TestNodeStoreLoadMissingFile(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "not-exist.json"))
	if err := store.Load(); err != nil {
		t.Fatalf("文件缺失时不应报错: %v", err)
	}
}

// TestNodeStoreRemove 验证删除记录后端口可以被其他客户端复用
func TestNodeStoreRemove(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	base := 31080
	first := reserveNode(t, store, "10.0.6.1", "removable", base, 1)
	store.Remove(nodeID("10.0.6.1"))
	if _, ok := store.Record(nodeID("10.0.6.1")); ok {
		t.Fatal("删除后不应还能查到记录")
	}
	second := reserveNode(t, store, "10.0.6.2", "other", base, 1)
	if second != first {
		t.Fatalf("释放后应能复用端口 %d，实际 %d", first, second)
	}
}

// TestNodeStoreSnapshotIsCopy 验证快照是副本，改动不会回写存储
func TestNodeStoreSnapshotIsCopy(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	address := "10.0.7.1"
	clientID := nodeID(address)
	reserveNode(t, store, address, "snapshot", 32080, 10)
	snapshot := store.Snapshot()
	record := snapshot[clientID]
	record.Up = 12345
	snapshot[clientID] = record
	stored, _ := store.Record(clientID)
	if stored.Up != 0 {
		t.Fatalf("快照改动不应影响存储，实际 %d", stored.Up)
	}
}

// TestNodeStoreConcurrentReserve 验证并发分配不会撞车
func TestNodeStoreConcurrentReserve(t *testing.T) {
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	base := 33080
	const workers = 16
	results := make(chan int, workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			address := "10.1.0." + string(rune('a'+index))
			port, _ := store.Reserve(nodeID(address), address, "node", base, workers, noPortInUse)
			results <- port
		}(i)
	}
	seen := make(map[int]bool, workers)
	for i := 0; i < workers; i++ {
		port := <-results
		if port == 0 {
			t.Fatal("并发分配不应失败")
		}
		if seen[port] {
			t.Fatalf("端口 %d 被重复分配", port)
		}
		seen[port] = true
	}
}

// TestNodeStoreAvoidsRealListener 验证真实占用的端口会被避开
func TestNodeStoreAvoidsRealListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer listener.Close()
	busyPort := listener.Addr().(*net.TCPAddr).Port
	if busyPort+1 > app.MaxSocksPort {
		t.Skip("随机端口已到上限，无法验证顺延")
	}
	store := server.NewNodeStore(filepath.Join(t.TempDir(), "clients.json"))
	inUse := func(port int) bool { return port == busyPort }
	port, err := store.Reserve(nodeID("10.0.8.1"), "10.0.8.1", "listener", busyPort, 2, inUse)
	if err != nil {
		t.Fatalf("分配失败: %v", err)
	}
	if port != busyPort+1 {
		t.Fatalf("应顺延到 %d，实际 %d", busyPort+1, port)
	}
}
