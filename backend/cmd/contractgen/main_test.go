package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedFileIsUpToDate 把「生成物是否过期」纳入 go test：改了注册表却忘记重新
// 生成时，普通测试就会失败，不必依赖 CI 额外跑 make contract-check。
func TestGeneratedFileIsUpToDate(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	target := filepath.Join(root, "frontend", "src", "types", "runtime", "event-contract.ts")
	existing, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取生成物失败（是否忘记生成？）：%v", err)
	}
	if string(existing) != render() {
		t.Fatalf("%s 与 internal/events 注册表不一致；请运行 go run ./cmd/contractgen 重新生成", target)
	}
}
