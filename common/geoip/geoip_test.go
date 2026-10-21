// geoip 包测试：用仓库内的离线归属地库验证解析结果，库文件缺失时跳过
package geoip

import (
	"path/filepath"
	"testing"
)

// openDatabase 打开仓库内的 IPv4 归属地库，文件缺失或结构不符时跳过用例
func openDatabase(t *testing.T) *Database {
	t.Helper()
	database, err := Open(filepath.Join("..", "..", "assets", "ip2region_v4.xdb"))
	if err != nil {
		t.Skipf("离线归属地库不可用，跳过用例: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

// openLibrary 装载仓库内的 IPv4 与 IPv6 两份归属地库，任一版本不可用时跳过用例
func openLibrary(t *testing.T) *Library {
	t.Helper()
	library := NewLibrary()
	t.Cleanup(func() {
		_ = library.Close()
	})
	for _, name := range []string{"ip2region_v4.xdb", "ip2region_v6.xdb"} {
		if err := library.Load(filepath.Join("..", "..", "assets", name)); err != nil {
			t.Skipf("离线归属地库 %s 不可用，跳过用例: %v", name, err)
		}
	}
	return library
}

// TestParse 验证归属地字段按位置拆解并跳过缺失值
func TestParse(t *testing.T) {
	region := Parse("中国|广东省|深圳市|电信|CN")
	if region.Country != "中国" || region.Province != "广东省" || region.City != "深圳市" || region.ISP != "电信" {
		t.Fatalf("字段拆解结果不符: %+v", region)
	}
	if text := region.Display(); text != "中国 广东省 深圳市 电信" {
		t.Fatalf("展示文本不符: %s", text)
	}
	named := Parse("0|California|0|Google LLC|US")
	if named.Country != "US" || named.Province != "California" || named.City != "" {
		t.Fatalf("国家名缺失时未回落到国家代码: %+v", named)
	}
	missing := Parse("0|0|0|0|0")
	if text := missing.Display(); text != UnknownRegion {
		t.Fatalf("全缺失时应展示 %s，实际 %s", UnknownRegion, text)
	}
	repeated := Parse("北京市|北京市|0|联通|CN")
	if text := repeated.Display(); text != "北京市 联通" {
		t.Fatalf("重复字段应只保留一次，实际 %s", text)
	}
}

// TestLookupChineseAddress 验证国内地址能查到国家与省份
func TestLookupChineseAddress(t *testing.T) {
	database := openDatabase(t)
	region, found := database.Lookup("114.114.114.114")
	if !found {
		t.Fatal("国内地址应能查到归属地")
	}
	t.Logf("114.114.114.114 -> %s", region.Display())
	if region.Country != "中国" {
		t.Fatalf("国家应为中国，实际 %s", region.Country)
	}
	if region.Province == "" {
		t.Fatalf("应能查到省份: %+v", region)
	}
}

// TestLookupOverseasAddress 验证境外地址能查到国家
func TestLookupOverseasAddress(t *testing.T) {
	database := openDatabase(t)
	region, found := database.Lookup("8.8.8.8")
	if !found {
		t.Fatal("境外地址应能查到归属地")
	}
	t.Logf("8.8.8.8 -> %s", region.Display())
	if region.Country == "" || region.Country == "中国" {
		t.Fatalf("国家解析异常: %+v", region)
	}
}

// TestLookupCachedRepeatedly 验证同一地址重复查询结果一致
func TestLookupCachedRepeatedly(t *testing.T) {
	database := openDatabase(t)
	first, firstFound := database.Lookup("223.5.5.5")
	second, secondFound := database.Lookup("223.5.5.5")
	if firstFound != secondFound || first != second {
		t.Fatalf("重复查询结果不一致: %v/%v 与 %v/%v", first, firstFound, second, secondFound)
	}
}

// TestLookupRejectsInvalidAddress 验证非法地址与 IPv6 地址在 IPv4 库上按未收录处理
func TestLookupRejectsInvalidAddress(t *testing.T) {
	database := openDatabase(t)
	for _, address := range []string{"", "not-an-ip", "2001:4860:4860::8888"} {
		if _, found := database.Lookup(address); found {
			t.Fatalf("地址 %q 不应有归属地", address)
		}
	}
}

// TestLookupIPv4OnIPv6Database 验证 IPv6 库不接管 IPv4 地址与 IPv4 映射地址
func TestLookupIPv4OnIPv6Database(t *testing.T) {
	database, err := Open(filepath.Join("..", "..", "assets", "ip2region_v6.xdb"))
	if err != nil {
		t.Skipf("IPv6 离线归属地库不可用，跳过用例: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	if database.IPVersion() != ipVersionIPv6 {
		t.Fatalf("应识别为 IPv6 库，实际版本为 %d", database.IPVersion())
	}
	for _, address := range []string{"114.114.114.114", "::ffff:114.114.114.114"} {
		if _, found := database.Lookup(address); found {
			t.Fatalf("地址 %q 不应由 IPv6 库解析", address)
		}
	}
}

// TestLibraryLookupByAddressFamily 验证 IPv4 与 IPv6 地址分别由同版本的库解析
func TestLibraryLookupByAddressFamily(t *testing.T) {
	library := openLibrary(t)
	cases := []struct {
		// address 是待查询地址
		address string
		// wantCountry 是期望的国家名
		wantCountry string
	}{
		{address: "114.114.114.114", wantCountry: "中国"},
		{address: "240e:4c:4008::1", wantCountry: "中国"},
	}
	for _, item := range cases {
		region, found := library.Lookup(item.address)
		if !found {
			t.Fatalf("地址 %s 应能查到归属地", item.address)
		}
		t.Logf("%s -> %s", item.address, region.Display())
		if region.Country != item.wantCountry {
			t.Fatalf("地址 %s 的国家应为 %s，实际为 %s", item.address, item.wantCountry, region.Country)
		}
	}
}

// TestLibraryRejectsDuplicateVersion 验证同一 IP 版本重复装载时报错且不替换已装载的库
func TestLibraryRejectsDuplicateVersion(t *testing.T) {
	library := NewLibrary()
	t.Cleanup(func() {
		_ = library.Close()
	})
	path := filepath.Join("..", "..", "assets", "ip2region_v4.xdb")
	if err := library.Load(path); err != nil {
		t.Skipf("离线归属地库不可用，跳过用例: %v", err)
	}
	if err := library.Load(path); err == nil {
		t.Fatal("重复装载同一版本的库应报错")
	}
	if paths := library.Paths(); len(paths) != 1 || paths[0] != path {
		t.Fatalf("重复装载不应改动已装载的库: %v", paths)
	}
}

// TestLibraryWithoutLoad 验证未装载任何库时查询、取路径与关闭都不出错
func TestLibraryWithoutLoad(t *testing.T) {
	var library *Library
	if _, found := library.Lookup("8.8.8.8"); found {
		t.Fatal("未装载库时不应返回归属地")
	}
	if paths := library.Paths(); len(paths) != 0 {
		t.Fatalf("未装载库时路径应为空: %v", paths)
	}
	if err := library.Close(); err != nil {
		t.Fatalf("未装载库时关闭不应报错: %v", err)
	}
}

// TestLookupWithoutDatabase 验证未打开库时统一返回未收录且不 panic
func TestLookupWithoutDatabase(t *testing.T) {
	var database *Database
	if _, found := database.Lookup("8.8.8.8"); found {
		t.Fatal("未打开库时不应返回归属地")
	}
	if database.Path() != "" {
		t.Fatal("未打开库时路径应为空")
	}
	if err := database.Close(); err != nil {
		t.Fatalf("未打开库时关闭不应报错: %v", err)
	}
}

// TestOpenRejectsMissingFile 验证文件缺失与结构不符时报错
func TestOpenRejectsMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing.xdb")); err == nil {
		t.Fatal("文件缺失时 Open 应报错")
	}
}
