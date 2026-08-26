//go:build !windows

package commands

import "context"

// readLegacyConsoleLine 在非 windows 平台不存在传统控制台问题（终端都支持
// VT 输入序列），直接回退 buffered 读取。
func readLegacyConsoleLine(ctx context.Context) (string, bool, error) {
	return "", false, nil
}
