//go:build !windows

package winconsole

// EnsureConsoleUTF8Output 非 Windows 平台为空操作。
func EnsureConsoleUTF8Output() func() { return nil }