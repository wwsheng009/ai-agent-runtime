//go:build !windows && (!unix || aix || solaris)

package agentconfig

// routingFileOSLockSupported 标记本平台没有跨进程锁实现（测试据此跳过跨进程用例）。
const routingFileOSLockSupported = false

// acquireRoutingFileOSLock 是跨进程锁的平台兜底实现（no-op）。
//
// 覆盖 aix / solaris（标准库没有 syscall.Flock，其记录锁走 fcntl）以及 plan9、
// js/wasm 等非 unix/windows 目标：这些平台直接静默降级为「仅进程内锁」，即方案
// §12 R4 登记的跨进程「后写胜出」语义。这样做的代价是这些平台没有跨进程互斥，
// 收益是本包不为极冷门目标引入额外的平台分支与依赖，且所有目标都能编译通过。
func acquireRoutingFileOSLock(string) (func(), bool) {
	return nil, false
}
