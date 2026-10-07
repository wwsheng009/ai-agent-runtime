package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// S5-4 回归栅栏：生产代码（ui 树 + commands 树）不得再出现任何 CSI 3J
// （清空 scrollback）字节序列。S3 删除了全部物理清屏路径；本测试把"以后也
// 不许重新引入"钉在源码层，任何 \x1b[3J / \033[3J / \u001b[3J 形式都会命中
// `[3J`。测试文件（含本文件的历史断言）不在扫描范围内。
func TestNoScrollbackClearSequenceInProductionSources(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiDir := filepath.Dir(currentFile)
	roots := []string{uiDir, filepath.Join(uiDir, "..", "commands")}

	var hits []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(data), "[3J") {
				hits = append(hits, filepath.ToSlash(path))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("CSI 3J (scrollback clear) reintroduced in production sources:\n%s", strings.Join(hits, "\n"))
	}
}
