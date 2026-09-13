package usageanalytics

import (
	"fmt"
	"os"
)

// degradeWarn 是统一用量分析的降级诊断出口（默认 stderr；测试可替换以断言
// 降级不再静默）。
//
// 背景（回归事故）：分析库由每个 aicli / runtime-server 进程各自打开，多个
// 进程并发启动时会同时 migrate 同一个库，失败者拿到 "database is locked"；
// 调用方随后静默返回 nil，该进程在整个生命周期内不再采集任何用量行——
// 统一分析库因此几乎为空，Web 缓存页（优先读分析库）表现为"会话恢复后
// 缓存历史没有加载"，而日志里没有任何线索，只能靠翻数据库取证。
//
// 降级必须留痕：打开/挂载失败说明该进程不会写库；写失败说明该请求的用量行
// 已经丢失。两类事件各自只提示一次（必要时按计数周期提醒），避免刷屏。
var degradeWarn = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Warning: usage analytics "+format+"\n", args...)
}
