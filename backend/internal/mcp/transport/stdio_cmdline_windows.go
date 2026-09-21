//go:build windows

package transport

import (
	"os/exec"
	"syscall"
)

// applyRawCmdLine 把「已经按 cmd.exe 规则拼好的整条命令行」原样交给
// CreateProcess（等价于 Node 的 windowsVerbatimArguments=true）。
//
// 必须只修改 CmdLine 字段：ProcessGuard.Bind 会在同一个 SysProcAttr 上 OR 上
// HideWindow / CREATE_NEW_PROCESS_GROUP 等标志，整体替换会丢掉这些安全设置。
// 调用时机：guard.Bind 之前或之后皆可，但必须在 cmd.Start() 之前。
func applyRawCmdLine(cmd *exec.Cmd, raw string) {
	if cmd == nil || raw == "" {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = raw
}
