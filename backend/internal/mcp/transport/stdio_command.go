package transport

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// resolveStdioCommand 把 stdio 命令解析成 exec 可直接执行的形式，并返回最终 argv。
//
// Windows 上 npx / uvx 等常见 MCP 启动器是 .cmd 垫片脚本：CreateProcess 无法直接
// 执行批处理，`exec.Command("npx", ...)` 会以 "not a valid application" 失败。
// 这里按 PATHEXT（exec.LookPath 语义）解析出真实脚本，并用 `cmd.exe /c <script>`
// 包装；其余平台与非垫片命令原样返回（解析失败时保留原命令，让错误在启动阶段
// 以原始形式暴露）。
func resolveStdioCommand(command string, args []string) (string, []string) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return command, args
	}
	if runtime.GOOS != "windows" {
		return command, args
	}

	target := trimmed
	if filepath.Ext(target) == "" {
		resolved, err := exec.LookPath(target)
		if err != nil {
			return command, args
		}
		target = resolved
	}
	if !isWindowsBatchShim(target) {
		return target, args
	}
	wrapped := make([]string, 0, len(args)+2)
	wrapped = append(wrapped, "/c", target)
	wrapped = append(wrapped, args...)
	return "cmd.exe", wrapped
}

func isWindowsBatchShim(path string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".cmd", ".bat":
		return true
	default:
		return false
	}
}
