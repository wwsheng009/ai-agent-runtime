package ui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestScrollbackResetIsNotReachableFromProductionFiles is the enforcement half
// of the destructive-plan fence. TerminalTransactionPlan.resetScrollback is
// unexported, so the only cross-package route to a scrollback-replacing plan is
// ComposeScrollbackReconciliationPlanForDebug; this walks the non-test files
// under cmd/aicli and fails when one of them names it, in the same
// inventory-gate style as the chat direct-writer baseline.
func TestScrollbackResetIsNotReachableFromProductionFiles(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("caller information unavailable")
	}
	root := filepath.Dir(file)
	for filepath.Base(root) != "aicli" {
		parent := filepath.Dir(root)
		if parent == root {
			t.Skipf("cmd/aicli root not found above %s", file)
		}
		root = parent
	}

	const symbol = "ComposeScrollbackReconciliationPlanForDebug"
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for index, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(line, symbol) {
				continue
			}
			if strings.Contains(line, "func "+symbol) {
				continue
			}
			offenders = append(offenders, fmt.Sprintf("%s:%d", path, index+1))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production files: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("production files must not build destructive scrollback plans: %v", offenders)
	}
}
