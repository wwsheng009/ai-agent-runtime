package sqlitedriver

import (
	"os"
	"strings"
	"sync"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// warmOnce 避免重复发起预热 goroutine。sqlite3.Initialize 自己也有 sync.Once，
// 这里只是让重复调用变成零开销的空操作。
var warmOnce sync.Once

// WarmAsync 在后台解码并编译 SQLite 的 Wasm 模块（wazero JIT），把这段成本从
// 「第一个访问会话存储的请求」挪到启动阶段的重叠窗口里。
//
// 为什么需要预热：本仓库的会话存储是惰性打开的（path-backed 存储在第一次真正
// 读写之前不建立连接，见 internal/chat 的 LazyUntilFirstUse 测试），所以启动
// 路径本身不会付这笔编译成本；它会被推到第一个真正用存储的请求上——live 实测
// 该请求因此多花 1.07s，profile 全部落在 ncruces/go-sqlite3 的 compileSQLite。
//
// 为什么用 sqlite3.Initialize 而不是「开一个连接跑一次 SELECT 1」：它是该驱动
// 官方提供的预热入口（文档原话："potentially slow, so you may want to call it
// at a more convenient time"），只做编译——不开连接、不建文件、不碰任何 DSN、
// 不涉及文件锁，因此与本仓库的重放/授权语义、UI 锁完全隔离。编译产物是进程级
// 的（包级 instance，sync.Once 保护），预热之后所有后续连接直接复用，不会重复
// 编译——无论谁先触发，都只编译一次。
//
// 失败只记日志、不影响启动：预热不是启动的必要条件。编译失败时错误会留在
// instance.err 上，首个真正使用存储的请求会如实拿到它，语义与未预热时一致。
//
// AICLI_SQLITE_WARMUP=0/false/no/off 可关闭预热（A/B 对比与排障用）。
func WarmAsync() {
	if sqliteWarmupDisabled() {
		return
	}
	warmOnce.Do(func() {
		go func() {
			start := time.Now()
			if err := sqlite3.Initialize(); err != nil {
				logpkg.Debugf("sqlite wasm 预热失败: %v", err)
				return
			}
			logpkg.Debugf("sqlite wasm 预热完成: +%s", time.Since(start).Round(time.Millisecond))
		}()
	})
}

func sqliteWarmupDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_SQLITE_WARMUP"))) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}
