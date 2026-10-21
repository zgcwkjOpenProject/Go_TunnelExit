// log 提供带时间戳的应用日志：历史日志按天归档，早于保留天数的归档在启动与跨天时删除
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 日志归档命名规则
const (
	logDateLayout = "2006-01-02" // 归档日志文件名里的日期格式，按本机时区判断跨天
	logSuffix     = ".log"       // 日志文件扩展名，归档文件在同名前缀后接日期
)

// logState 是全局日志状态，写文件与终端前需要持有它的锁
type logState struct {
	sync.Mutex
	file     *os.File // 当前日志文件，未启用文件日志时为 nil
	path     string   // 当前日志文件路径
	date     string   // 当前文件覆盖的本地日期，跨天后先归档再新建
	keepDays int      // 归档日志保留天数
}

// logger 是全局日志实例，日志参数在 SetupLogging 里按环境变量确定
var logger = logState{
	keepDays: DefaultLogKeepDays,
}

// terminal 缓存标准输出是否连着终端，只判断一次避免每行日志都做系统调用
var terminal = struct {
	sync.Once
	enabled bool
}{}

// isTerminal 判断标准输出是否直接连着终端
//
// 重定向到文件或管道时只写文件，否则套件启动脚本会把标准输出追加到同一个日志文件造成每行重复
func isTerminal() bool {
	terminal.Do(func() {
		info, err := os.Stdout.Stat()
		terminal.enabled = err == nil && info.Mode()&os.ModeCharDevice != 0
	})
	return terminal.enabled
}

// SetupLogging 打开日志文件并做一次启动清理，路径为空或保留天数为零时只使用标准输出
func SetupLogging(path string) string {
	if path == "" {
		return ""
	}
	logger.Lock()
	defer logger.Unlock()
	logger.keepDays = LogKeepDays()
	if logger.keepDays == 0 {
		return ""
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "无法创建日志目录 %s: %v\n", dir, err)
		return ""
	}
	logger.path = path
	// 上次运行留下的日志先按最后写入日期归档再清理过期归档，避免把旧日志当成当天日志继续追加
	logger.archiveLocked()
	logger.pruneLocked(time.Now())
	if err := logger.openLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "无法打开日志文件 %s: %v\n", path, err)
		logger.path = ""
		return ""
	}
	return path
}

// LogSummary 返回日志位置与保留策略的说明文本，未启用日志文件时说明只输出到标准输出
func LogSummary(path string) string {
	if path == "" {
		return "未写日志文件，只输出到标准输出"
	}
	return fmt.Sprintf("%s（按天归档，保留 %d 天）", path, LogKeepDays())
}

// Logf 写入一条带时间戳的日志，前台运行时同时显示到终端
func Logf(tag string, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), tag, message)
	logger.Lock()
	defer logger.Unlock()
	if logger.file == nil {
		_, _ = os.Stdout.WriteString(line)
		return
	}
	logger.rotateLocked()
	if logger.file == nil {
		_, _ = os.Stdout.WriteString(line)
		return
	}
	if _, err := logger.file.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "写入日志文件 %s 失败: %v\n", logger.path, err)
	}
	if isTerminal() {
		_, _ = os.Stdout.WriteString(line)
	}
}

// LogFatalf 写入日志并终止进程，用于无法继续的启动错误
func LogFatalf(tag string, format string, args ...any) {
	Logf(tag, format, args...)
	os.Exit(1)
}

// CloseLogging 关闭日志文件，供进程退出前调用
func CloseLogging() {
	logger.Lock()
	defer logger.Unlock()
	if logger.file != nil {
		_ = logger.file.Close()
		logger.file = nil
	}
}

// openLocked 以追加方式打开日志文件，并记下它覆盖的日期
//
// 调用方必须已持有 logger 锁，写入前记下日期是为了跨天时能归档出当天对应的文件名
func (l *logState) openLocked() error {
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	l.file = file
	l.date = time.Now().Format(logDateLayout)
	return nil
}

// rotateLocked 在跨天时把当前日志归档，随后打开新一天的文件
//
// 调用方必须已持有 logger 锁且日志文件已打开，按天归档让保留天数有明确含义
func (l *logState) rotateLocked() {
	today := time.Now().Format(logDateLayout)
	if l.date == today {
		return
	}
	_ = l.file.Close()
	l.file = nil
	l.archiveLocked()
	l.pruneLocked(time.Now())
	if err := l.openLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "无法打开日志文件 %s: %v\n", l.path, err)
	}
}

// archiveLocked 把当前日志文件按最后写入日期改名归档
//
// 日期取自文件修改时间而不是上次打开时间：停机跨天后重启归档的是停机前的日志，
// 同一天内重启则继续追加同一个文件，一天最多留下一个归档
func (l *logState) archiveLocked() {
	info, err := os.Stat(l.path)
	if err != nil {
		return
	}
	if info.Size() == 0 {
		_ = os.Remove(l.path)
		return
	}
	written := info.ModTime()
	if written.Format(logDateLayout) == time.Now().Format(logDateLayout) {
		return
	}
	target := l.archivePath(written)
	// 同名归档只保留最新的一份，避免同一天反复归档时留下多个文件
	_ = os.Remove(target)
	if err := os.Rename(l.path, target); err != nil {
		fmt.Fprintf(os.Stderr, "归档日志文件 %s 失败: %v\n", l.path, err)
	}
}

// archivePath 返回指定日期对应的归档日志路径，日志名去掉扩展名后接日期
func (l *logState) archivePath(written time.Time) string {
	return strings.TrimSuffix(l.path, logSuffix) + "-" + written.Format(logDateLayout) + logSuffix
}

// pruneLocked 删除早于保留天数的归档日志
//
// 只清理本程序命名的归档文件，目录里其他文件一律不动
func (l *logState) pruneLocked(now time.Time) {
	if l.keepDays <= 0 {
		return
	}
	base := strings.TrimSuffix(filepath.Base(l.path), logSuffix)
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(l.path), base+"-*"+logSuffix))
	if err != nil {
		return
	}
	deadline := now.AddDate(0, 0, -l.keepDays)
	for _, name := range matches {
		written, err := time.ParseInLocation(logDateLayout, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), base+"-"), logSuffix), time.Local)
		if err != nil {
			continue
		}
		if written.Before(deadline) {
			_ = os.Remove(name)
		}
	}
}
