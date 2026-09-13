package cacheanalytics

import (
	"fmt"
	"os"
)

// degradeWarn 是持久化降级诊断出口（默认 stderr；测试可替换以断言降级不再静默）。
//
// 背景（回归事故）：镜像表不可用（旧库未迁移出 cache_requests、数据库被锁、
// 运行期退回内存 store）时，加载失败此前被静默吞掉，且会话被永久标记为
// "已回放"——于是"进程重启/恢复会话后缓存历史为空"既没有日志、也不会重试，
// 与"确实没有历史记录"在现象上完全无法区分，排查只能靠翻数据库。
//
// 持久化降级必须留下痕迹：写入失败（该请求历史在进程结束后不可回放）与
// 加载失败（历史暂不可见，后续查询会重试）各提示一次，避免每请求刷屏。
var degradeWarn = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Warning: cache analytics "+format+"\n", args...)
}
