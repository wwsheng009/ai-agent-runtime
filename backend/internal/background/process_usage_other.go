//go:build !windows && !linux

package background

// sampleProcessUsage is unsupported on this platform.
func sampleProcessUsage(pid int) (processUsage, bool) { return processUsage{}, false }
