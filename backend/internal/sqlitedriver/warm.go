package sqlitedriver

import (
	"os"
	"strings"
)

// WarmAsync 保留为兼容入口，但自 ncruces/go-sqlite3 v0.33 起它已经没有可预热的
// 工作：新驱动不再把 SQLite 编译成 Wasm（wazero/JIT 路径连同 sqlite3.Initialize
// 一起被移除），引擎是纯 Go 翻译实现，进程启动时由 Go runtime 直接加载，不存在
// 「第一次访问存储时现编译 1 秒」的问题。保留该函数是为了不改变 cmd/aicli 的启动
// 调用点与 AICLI_SQLITE_WARMUP 的排障契约（见 warm_test.go 与
// docs/e2e/resume-m1-ab-measurement.md）。
//
// AICLI_SQLITE_WARMUP=0/false/no/off 仍然被解析，只是不再改变行为。
func WarmAsync() {}

// sqliteWarmupDisabled 保留原有环境变量语义（只有明确关闭才算关闭）。
func sqliteWarmupDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_SQLITE_WARMUP"))) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}
