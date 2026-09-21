//go:build !windows

package transport

import "os/exec"

// applyRawCmdLine 在非 Windows 平台是空操作：RawCmdLine 只用于 Windows 上
// .cmd/.bat 垫片的 cmd.exe 包装路径。
func applyRawCmdLine(cmd *exec.Cmd, raw string) {}
