package agentconfig

import (
	"os"
	"testing"
)

// chdirTest 是 Go 1.24+ 的 testing.T.Chdir 在 Go 1.21（win7compat 构建）下的等价实现：
// 切换进程工作目录，并在测试结束时恢复。
//
// 为什么自带而不直接用 t.Chdir：Windows 7 兼容构建（scripts/build.ps1 -Target win7）
// 固定使用 Go 1.21.4 工具链编译并运行测试，该版本没有 t.Chdir；而这些测试文件同时
// 属于 win7 测试阶段，直接调用会让测试二进制编译失败。
//
// 语义对齐 t.Chdir：目录不存在时立即 Fatal；不得在并行测试中调用（t.Chdir 同样
// 禁止并行，会 panic）。
func chdirTest(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("chdir %q: getwd: %v", dir, err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %q: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("chdir restore %q: %v", previous, err)
		}
	})
}
