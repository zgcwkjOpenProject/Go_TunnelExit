// key_test 验证服务端密钥对的生成、持久化与客户端按公钥校验身份的链路
//
// 隧道端口只承载 TLS 连接，因此"公钥不匹配就连不上"是这一层最关键的行为约定
package test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"tunnelexit/app"
	"tunnelexit/client"
	"tunnelexit/server"
)

// TestServerKeyGeneratedOnceAndPersisted 验证首次启动生成密钥对，重启后沿用同一份
func TestServerKeyGeneratedOnceAndPersisted(t *testing.T) {
	appDir := t.TempDir()
	directory := filepath.Join(appDir, "server")

	first, publicKey, generated, err := app.LoadServerKey(directory)
	if err != nil {
		t.Fatalf("首次装载密钥失败: %v", err)
	}
	if !generated {
		t.Fatal("首次装载应标记为新生成")
	}
	if len(first.Certificate) == 0 {
		t.Fatal("首次装载未返回证书")
	}
	if publicKey == "" {
		t.Fatal("首次装载未返回公钥文本")
	}
	for _, path := range []string{app.ServerKeyPath(directory), app.ServerCertPath(directory)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("密钥文件 %s 未生成: %v", path, err)
		}
		// Windows 没有 POSIX 权限位，Stat 一律回报可读写，只有类 Unix 平台才能校验权限
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("密钥文件 %s 权限为 %v，应为 0600", path, info.Mode().Perm())
		}
	}

	if _, err := os.Stat(app.KeysDirPath(directory)); err != nil {
		t.Fatalf("密钥目录 %s 未生成: %v", app.KeysDirPath(directory), err)
	}
	publicKeyBytes, err := os.ReadFile(app.ServerPublicKeyPath(directory))
	if err != nil {
		t.Fatalf("公钥文件 %s 未生成: %v", app.ServerPublicKeyPath(directory), err)
	}
	// 公钥文件是给用户直接用记事本打开复制的，内容必须与证书里的公钥完全一致
	if string(publicKeyBytes) != publicKey {
		t.Fatal("公钥文件内容与装载返回的公钥文本不一致")
	}
	if _, err := app.ParsePublicKey(string(publicKeyBytes)); err != nil {
		t.Fatalf("公钥文件内容无法按 PEM 解析: %v", err)
	}

	// 用户可能手动删掉公钥文件，重新装载时应补齐一份，已下发的公钥不能因此失效
	if err := os.Remove(app.ServerPublicKeyPath(directory)); err != nil {
		t.Fatalf("删除公钥文件失败: %v", err)
	}
	_, again, generated, err := app.LoadServerKey(directory)
	if err != nil {
		t.Fatalf("再次装载密钥失败: %v", err)
	}
	if generated {
		t.Fatal("密钥文件已存在时不应重新生成")
	}
	if again != publicKey {
		t.Fatal("重启后公钥发生了变化")
	}
	restored, err := os.ReadFile(app.ServerPublicKeyPath(directory))
	if err != nil {
		t.Fatalf("公钥文件未被补齐: %v", err)
	}
	if string(restored) != publicKey {
		t.Fatal("补齐的公钥文件内容与公钥文本不一致")
	}
}

// TestServerKeyRegeneratesWhenBroken 验证密钥文件损坏时自动重新生成而不是启动失败
func TestServerKeyRegeneratesWhenBroken(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "server")
	if _, _, _, err := app.LoadServerKey(directory); err != nil {
		t.Fatalf("首次生成密钥失败: %v", err)
	}
	if err := os.WriteFile(app.ServerKeyPath(directory), []byte("不是密钥"), 0o600); err != nil {
		t.Fatalf("写入损坏私钥失败: %v", err)
	}
	_, publicKey, generated, err := app.LoadServerKey(directory)
	if err != nil {
		t.Fatalf("损坏后重新生成失败: %v", err)
	}
	if !generated {
		t.Fatal("损坏的密钥应触发重新生成")
	}
	if publicKey == "" {
		t.Fatal("重新生成后未返回公钥文本")
	}
}

// TestClientTLSConfigRejectsMissingOrBrokenKey 验证公钥缺失或格式非法时提前报错
func TestClientTLSConfigRejectsMissingOrBrokenKey(t *testing.T) {
	if _, err := app.ClientTLSConfig("   "); !errors.Is(err, app.ErrKeyMissing) {
		t.Fatalf("空公钥应返回 ErrKeyMissing，实际 %v", err)
	}
	if _, err := app.ClientTLSConfig("随便一段文本"); !errors.Is(err, app.ErrKeyInvalid) {
		t.Fatalf("非法公钥应返回 ErrKeyInvalid，实际 %v", err)
	}
}

// TestClientRejectsMismatchedPublicKey 验证客户端持有其他主机的公钥时无法接入
func TestClientRejectsMismatchedPublicKey(t *testing.T) {
	root := t.TempDir()
	serverDir := filepath.Join(root, "server")
	socksBase := freePortRange(t, testSocksPortCount)
	port := freePortOutsideRange(t, socksBase, testSocksPortCount)
	certificate, _ := testKeyPair(t, serverDir)
	// 另一对密钥模拟"客户端拿到的是别的服务端公钥"
	_, otherPublicKey := testKeyPair(t, filepath.Join(root, "other"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance := server.New(testServerConfig(port, socksBase), serverDir, certificate)
	if err := instance.Start(ctx); err != nil {
		t.Fatalf("启动服务端失败: %v", err)
	}
	defer instance.Stop()

	node := client.New(testClientConfig("127.0.0.1", port, app.NewClientID(), "e2e-wrong-key", otherPublicKey))
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	err := node.Run(runCtx)
	if err == nil {
		t.Fatal("公钥不匹配时不应连接成功")
	}
	if !errors.Is(err, app.ErrKeyMismatch) {
		t.Fatalf("公钥不匹配应返回 ErrKeyMismatch，实际 %v", err)
	}
}

// TestClientRejectsMissingPublicKey 验证客户端未填公钥时连接直接失败
func TestClientRejectsMissingPublicKey(t *testing.T) {
	root := t.TempDir()
	serverDir := filepath.Join(root, "server")
	socksBase := freePortRange(t, testSocksPortCount)
	port := freePortOutsideRange(t, socksBase, testSocksPortCount)
	certificate, _ := testKeyPair(t, serverDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance := server.New(testServerConfig(port, socksBase), serverDir, certificate)
	if err := instance.Start(ctx); err != nil {
		t.Fatalf("启动服务端失败: %v", err)
	}
	defer instance.Stop()

	node := client.New(testClientConfig("127.0.0.1", port, app.NewClientID(), "e2e-no-key", ""))
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	err := node.Run(runCtx)
	if !errors.Is(err, app.ErrKeyMissing) {
		t.Fatalf("未填公钥应返回 ErrKeyMissing，实际 %v", err)
	}
}

// TestServerRejectsNonTLSTunnelClient 验证隧道端口对明文连接直接拒绝
//
// 协议帧只在加密通道里出现，明文写入不会得到任何应答
func TestServerRejectsNonTLSTunnelClient(t *testing.T) {
	root := t.TempDir()
	serverDir := filepath.Join(root, "server")
	socksBase := freePortRange(t, testSocksPortCount)
	port := freePortOutsideRange(t, socksBase, testSocksPortCount)
	certificate, _ := testKeyPair(t, serverDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance := server.New(testServerConfig(port, socksBase), serverDir, certificate)
	if err := instance.Start(ctx); err != nil {
		t.Fatalf("启动服务端失败: %v", err)
	}
	defer instance.Stop()

	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatalf("连接隧道端口失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("明文数据不是 TLS 握手")); err != nil {
		t.Fatalf("写入明文数据失败: %v", err)
	}
	reply := make([]byte, 1)
	if _, err := conn.Read(reply); err == nil {
		t.Fatal("明文连接不应收到任何协议应答")
	}

	// 明文被拒后服务端仍可正常工作，说明拒绝过程不会破坏监听
	tlsConn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("明文连接被拒后 TLS 握手失败: %v", err)
	}
	_ = tlsConn.Close()
}
