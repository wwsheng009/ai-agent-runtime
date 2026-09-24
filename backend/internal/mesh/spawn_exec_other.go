//go:build !windows

package mesh

import "syscall"

// spawnSysProcAttr detaches the spawned node from the requesting process:
// Setsid puts it in a new session, so it survives the caller's process group
// (and its terminal) going away.
func spawnSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
