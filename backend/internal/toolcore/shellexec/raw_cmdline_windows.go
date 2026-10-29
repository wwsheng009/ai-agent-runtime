//go:build windows

package shellexec

import (
	"os/exec"
	"syscall"
)

// applyRawCmdLine hands an already-quoted command line to CreateProcess.
//
// This is the Windows equivalent of Node's windowsVerbatimArguments. It must
// not replace SysProcAttr: ProcessGuard.Bind adds HideWindow and
// CREATE_NEW_PROCESS_GROUP to the same struct before Start.
func applyRawCmdLine(cmd *exec.Cmd, raw string) {
	if cmd == nil || raw == "" {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = raw
}
