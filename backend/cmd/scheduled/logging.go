package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// 守护进程日志出口（S0 收尾）：Windows 服务模式下 stderr 不可见，运行期日志
// 必须有落盘出口，否则"谁在调度、为什么没投递"完全无从复盘。
//
// 口径：所有运行期日志走 logf（文件 + stderr 回显）；文件默认
// <home>/.aicli/logs/scheduled.log（与 runtime-server 日志同目录），可用
// --log-file 覆盖。打开失败只警告、不阻断守护进程（日志不是可用性前提）。
var daemonLog struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// defaultDaemonLogPath 返回默认日志文件路径（<home>/.aicli/logs/scheduled.log）。
func defaultDaemonLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".aicli", "logs", "scheduled.log")
}

// initDaemonLog 打开（或创建）日志文件并开始接收 logf 输出；空路径为 no-op。
func initDaemonLog(path string) {
	path = strings.TrimSpace(aiclipaths.ExpandUserPath(path))
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "scheduled: warning: cannot create log dir %s: %v\n", filepath.Dir(path), err)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scheduled: warning: cannot open log file %s: %v\n", path, err)
		return
	}
	daemonLog.mu.Lock()
	daemonLog.file = file
	daemonLog.path = path
	daemonLog.mu.Unlock()
}

// closeDaemonLog 关闭日志文件（幂等）。
func closeDaemonLog() {
	daemonLog.mu.Lock()
	file := daemonLog.file
	daemonLog.file = nil
	daemonLog.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
}

// logf 写一行运行期日志：文件带 UTC 时间戳；stderr 回显保持原文（前台可读）。
func logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	daemonLog.mu.Lock()
	file := daemonLog.file
	daemonLog.mu.Unlock()
	if file != nil {
		_, _ = fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
	}
	fmt.Fprintln(os.Stderr, line)
}
