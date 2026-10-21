// log_test 验证运行日志的按天归档、保留天数清理以及零保留天数不写文件
package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tunnelexit/app"
)

// logDateLayout 是日志归档文件名里的日期格式，与 app 包保持一致
const logDateLayout = "2006-01-02"

// TestLogArchivesAndPrunesByKeepDays 验证跨天日志按日期归档，早于保留天数的归档被删除
func TestLogArchivesAndPrunesByKeepDays(t *testing.T) {
	t.Setenv("TUNNELEXIT_LOG_KEEP_DAYS", "")
	appDir := t.TempDir()
	path := filepath.Join(appDir, app.DefaultLogFile)
	stale := filepath.Join(appDir, "tunnelexit-"+time.Now().AddDate(0, 0, -10).Format(logDateLayout)+".log")
	kept := filepath.Join(appDir, "tunnelexit-"+time.Now().AddDate(0, 0, -1).Format(logDateLayout)+".log")
	other := filepath.Join(appDir, "other.log")
	for _, name := range []string{stale, kept, other} {
		if err := os.WriteFile(name, []byte("2026-01-01 00:00:00 [test] 历史日志\n"), 0644); err != nil {
			t.Fatalf("准备测试文件 %s 失败: %v", name, err)
		}
	}
	// 上次运行的日志最后写入时间是两天前，启动时应归档成当天日期命名的文件
	previous := time.Now().AddDate(0, 0, -2)
	if err := os.WriteFile(path, []byte("2026-01-01 00:00:00 [test] 停机前的日志\n"), 0644); err != nil {
		t.Fatalf("准备当前日志失败: %v", err)
	}
	if err := os.Chtimes(path, previous, previous); err != nil {
		t.Fatalf("设置日志修改时间失败: %v", err)
	}

	if got := app.SetupLogging(path); got != path {
		t.Fatalf("日志初始化失败: %q", got)
	}
	defer app.CloseLogging()
	app.Logf("test", "启动后的日志")

	archive := filepath.Join(appDir, "tunnelexit-"+previous.Format(logDateLayout)+".log")
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("停机前的日志未按日期归档: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("超过保留天数的归档未被删除")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("保留天数内的归档被误删: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("目录里的其他文件被误删: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取当前日志失败: %v", err)
	}
	if !strings.Contains(string(data), "启动后的日志") {
		t.Fatalf("归档后新日志未正常写入: %q", string(data))
	}
}

// TestLogPathUsesSubdirectory 验证程序日志落在运行目录的日志子目录下
func TestLogPathUsesSubdirectory(t *testing.T) {
	appDir := t.TempDir()
	want := filepath.Join(appDir, app.DefaultLogDir, app.DefaultLogFile)
	if got := app.LogPath(appDir); got != want {
		t.Fatalf("日志路径 %q，期望 %q", got, want)
	}
}

// TestLogDisabledWhenKeepDaysZero 验证保留天数为零时不写日志文件，只保留标准输出
func TestLogDisabledWhenKeepDaysZero(t *testing.T) {
	t.Setenv("TUNNELEXIT_LOG_KEEP_DAYS", "0")
	defer app.CloseLogging()
	path := filepath.Join(t.TempDir(), app.DefaultLogFile)
	if got := app.SetupLogging(path); got != "" {
		t.Fatalf("保留天数为零时仍启用了日志文件: %q", got)
	}
	app.Logf("test", "不写文件的日志")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("保留天数为零时仍创建了日志文件")
	}
}
