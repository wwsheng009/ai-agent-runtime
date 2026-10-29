//go:build !windows

package shellexec

import "os/exec"

// Raw command lines are a Windows/CreateProcess concern. POSIX shells receive
// the command as a normal argv element.
func applyRawCmdLine(*exec.Cmd, string) {}
