// key 负责服务端密钥对的生成与装载，并给出两端共用的 TLS 配置
//
// 服务端首次启动生成 RSA 密钥对与自签名证书，客户端只凭服务端公钥确认对端身份，
// 因此隧道端口只承载 TLS 连接，登录帧与数据帧都跑在加密通道里，非法数据进不到协议层
package app

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 服务端密钥相关文件名与参数，密钥文件统一放在运行目录下的 keys 子目录
const (
	KeysDir             = "keys"                // 密钥文件所在子目录
	ServerKeyFile       = "private_key.pem"     // 服务端 RSA 私钥文件
	ServerCertFile      = "server.crt"          // 服务端自签名证书文件
	ServerPublicKeyFile = "public_key.pem"      // 服务端公钥文件，用户打开即可复制到客户端配置页
	serverSecretPerm    = 0o600                 // 私钥与证书权限，只允许属主读写
	keysDirPerm         = 0o755                 // 密钥目录权限，允许属主读写并进入
	publicKeyPerm       = 0o644                 // 公钥文件权限，放开只读以便直接打开复制
	rsaKeyBits          = 2048                  // RSA 密钥长度
	certificateValidFor = 3650 * 24 * time.Hour // 自签名证书有效期
)

// 密钥校验相关错误
var (
	ErrKeyMissing  = errors.New("尚未填写服务端公钥，请在配置页填入服务端 keys/public_key.pem 里的公钥文本") // 客户端未配置公钥
	ErrKeyInvalid  = errors.New("服务端公钥格式错误，应为 keys/public_key.pem 里的 PEM 文本")      // 客户端配置的公钥无法解析
	ErrKeyMismatch = errors.New("服务端公钥与服务端实际身份不一致")                                // 对端证书里的公钥与配置不符
)

// KeysDirPath 返回服务端密钥文件所在的目录
func KeysDirPath(appDir string) string {
	return filepath.Join(appDir, KeysDir)
}

// ServerKeyPath 返回服务端私钥文件路径
func ServerKeyPath(appDir string) string {
	return filepath.Join(KeysDirPath(appDir), ServerKeyFile)
}

// ServerCertPath 返回服务端证书文件路径
func ServerCertPath(appDir string) string {
	return filepath.Join(KeysDirPath(appDir), ServerCertFile)
}

// ServerPublicKeyPath 返回服务端公钥文件路径，该文件内容可直接整份填进客户端配置页
func ServerPublicKeyPath(appDir string) string {
	return filepath.Join(KeysDirPath(appDir), ServerPublicKeyFile)
}

// LoadServerKey 装载服务端密钥对，文件缺失或损坏时重新生成
//
// 公钥同时写入公钥文件供用户复制，generated 为 true 表示本次新生成，
// 沿用旧公钥的客户端必须重新填写新公钥才能接入
func LoadServerKey(appDir string) (tls.Certificate, string, bool, error) {
	keyPath := ServerKeyPath(appDir)
	certPath := ServerCertPath(appDir)
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		publicKey, keyErr := PublicKeyText(certificate)
		if keyErr == nil {
			// 公钥文件可能被用户删掉，每次装载都补齐一份
			if writeErr := writePublicKeyFile(ServerPublicKeyPath(appDir), publicKey); writeErr != nil {
				Logf("key", "写入服务端公钥文件失败: %v", writeErr)
			}
			return certificate, publicKey, false, nil
		}
		Logf("key", "服务端公钥导出失败，将重新生成密钥对: %v", keyErr)
	} else {
		Logf("key", "服务端密钥不可用，将重新生成密钥对: %v", err)
	}
	return generateServerKey(appDir)
}

// writePublicKeyFile 把公钥写入公钥文件，内容已经一致时跳过写入
func writePublicKeyFile(path string, publicKey string) error {
	if data, err := os.ReadFile(path); err == nil && string(data) == publicKey {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), keysDirPerm); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(publicKey), publicKeyPerm)
}

// PublicKeyText 把证书里的公钥导出为 PEM 文本，写入公钥文件供用户复制到客户端配置页
func PublicKeyText(certificate tls.Certificate) (string, error) {
	if len(certificate.Certificate) == 0 {
		return "", errors.New("证书内容为空")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// generateServerKey 生成 RSA 密钥对与自签名证书，连同公钥一起写入密钥目录
func generateServerKey(appDir string) (tls.Certificate, string, bool, error) {
	key, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return tls.Certificate{}, "", false, fmt.Errorf("生成 RSA 密钥失败: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "tunnelexit"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certificateValidFor),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// 客户端按公钥而不是域名比对身份，因此不填写 DNS 名称与 IP 地址
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", false, fmt.Errorf("签发自签名证书失败: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.MkdirAll(KeysDirPath(appDir), keysDirPerm); err != nil {
		return tls.Certificate{}, "", false, fmt.Errorf("创建密钥目录 %s 失败: %w", KeysDirPath(appDir), err)
	}
	if err := os.WriteFile(ServerKeyPath(appDir), keyPEM, serverSecretPerm); err != nil {
		return tls.Certificate{}, "", false, fmt.Errorf("写入服务端私钥 %s 失败: %w", ServerKeyPath(appDir), err)
	}
	if err := os.WriteFile(ServerCertPath(appDir), certPEM, serverSecretPerm); err != nil {
		return tls.Certificate{}, "", false, fmt.Errorf("写入服务端证书 %s 失败: %w", ServerCertPath(appDir), err)
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	publicKey, err := PublicKeyText(certificate)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	if err := writePublicKeyFile(ServerPublicKeyPath(appDir), publicKey); err != nil {
		return tls.Certificate{}, "", false, fmt.Errorf("写入服务端公钥 %s 失败: %w", ServerPublicKeyPath(appDir), err)
	}
	return certificate, publicKey, true, nil
}

// ServerTLSConfig 返回隧道端口的服务端 TLS 配置
//
// 客户端不必提交证书，密钥校验在握手之后的登录阶段按公钥比对完成，
// 因此握手本身保持轻量，且校验结果能通过登录应答把具体原因回告客户端
func ServerTLSConfig(certificate tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	}
}

// ClientTLSConfig 返回客户端连接隧道端口的 TLS 配置，按服务端公钥校验对端身份
//
// 服务端证书是自签名的，因此跳过系统根证书校验，改用配置页填写的公钥与对端证书公钥逐字节比对
func ClientTLSConfig(publicKeyText string) (*tls.Config, error) {
	publicKey, err := ParsePublicKey(publicKeyText)
	if err != nil {
		return nil, err
	}
	expectedKey, err := marshalPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // 跳过系统根证书校验，改用下面的公钥比对
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			peerKey, peerErr := peerPublicKey(rawCerts)
			if peerErr != nil {
				return peerErr
			}
			if !bytes.Equal(peerKey, expectedKey) {
				return ErrKeyMismatch
			}
			return nil
		},
	}, nil
}

// peerPublicKey 从对端证书链里取出第一张证书的公钥并编码为 DER
func peerPublicKey(rawCerts [][]byte) ([]byte, error) {
	if len(rawCerts) == 0 {
		return nil, errors.New("服务端未提供证书")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return nil, err
	}
	return marshalPublicKey(leaf.PublicKey)
}

// marshalPublicKey 把公钥编码成 DER 形式，便于逐字节比对身份
func marshalPublicKey(key any) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(key)
}

// ParsePublicKey 解析配置里的服务端公钥文本，为空或格式非法时返回对应错误
func ParsePublicKey(text string) (any, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, ErrKeyMissing
	}
	block, _ := pem.Decode([]byte(trimmed))
	if block == nil {
		return nil, ErrKeyInvalid
	}
	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, ErrKeyInvalid
	}
	return publicKey, nil
}

// IsKeyError 判断错误是否由客户端公钥不可用引起
//
// 这类错误按退避策略重试不会自愈，只能由用户改配置，调用方据此改用固定间隔重试并保持进程
func IsKeyError(err error) bool {
	return errors.Is(err, ErrKeyMissing) || errors.Is(err, ErrKeyInvalid) || errors.Is(err, ErrKeyMismatch)
}
