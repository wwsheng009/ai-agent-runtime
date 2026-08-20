package runtimeobserve

import "os"

// runtimeProcessID 返回当前进程 PID（用于快照 process.pid）。
func runtimeProcessID() int {
	return os.Getpid()
}
