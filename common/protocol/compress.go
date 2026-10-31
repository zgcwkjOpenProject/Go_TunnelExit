// compress 实现隧道流的 flate 压缩连接包装
package protocol

import (
	"compress/flate"
	"io"
	"net"
	"sync"
)

// CompressConn 在一条 net.Conn 上按方向分别压缩与解压
//
// 两个方向各持有独立的 flate 状态，读方向在首次读取时才创建解压器
type CompressConn struct {
	net.Conn
	writeMu  sync.Mutex // 串行化压缩器写入，flate 写状态不能并发
	writer   *flate.Writer
	readOnce sync.Once
	reader   io.Reader
}

// NewCompressConn 把连接包装成 flate 压缩通道
//
// 压缩级别是常量，创建失败只在级别非法时发生，失败时退回原始连接仅损失压缩率
func NewCompressConn(conn net.Conn) net.Conn {
	writer, err := flate.NewWriter(conn, flate.BestSpeed)
	if err != nil {
		return conn
	}
	return &CompressConn{Conn: conn, writer: writer}
}

// Read 读取并解压对端写出的压缩流
func (c *CompressConn) Read(p []byte) (int, error) {
	c.readOnce.Do(func() {
		c.reader = flate.NewReader(c.Conn)
	})
	return c.reader.Read(p)
}

// Write 压缩写出并立即冲刷，避免小数据滞留在压缩缓冲里拖高延迟
func (c *CompressConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.writer.Write(p); err != nil {
		return 0, err
	}
	if err := c.writer.Flush(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close 冲刷压缩器剩余数据后关闭底层连接
func (c *CompressConn) Close() error {
	c.writeMu.Lock()
	_ = c.writer.Close()
	c.writeMu.Unlock()
	return c.Conn.Close()
}
