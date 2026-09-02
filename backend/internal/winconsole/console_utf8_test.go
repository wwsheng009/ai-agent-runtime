package winconsole

import (
	"testing"
)

// TestEnsureConsoleUTF8OutputSafe 确保调用 EnsureConsoleUTF8Output 不会崩溃，
// 并且如果返回恢复函数，调用它也是安全的。
func TestEnsureConsoleUTF8OutputSafe(t *testing.T) {
	restore := EnsureConsoleUTF8Output()
	if restore != nil {
		restore()
	}
}