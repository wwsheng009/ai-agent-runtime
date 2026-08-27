package commands

import (
	"fmt"
	"os"
)

// aicliDiagf / aicliDiagln 是 chat 交互模式的 [aicli-diag] 诊断输出钩子。
// 默认直写 stderr；定义为变量以便测试或宿主嵌入方静默/重定向。
//
// 刻意放在非 chat*.go 命名的文件中：chat direct-writer inventory gate
// 只审计 chat*.go + command.go 里的直接终端写入，诊断日志属于调试/日志
// 路径而非交互渲染 writer，不进入迁移债清单。
var aicliDiagf = func(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
}

var aicliDiagln = func(args ...any) {
	_, _ = fmt.Fprintln(os.Stderr, args...)
}