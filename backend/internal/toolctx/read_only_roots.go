package toolctx

import (
	"context"
	"strings"
)

// 本文件实现「只读外部根」（F5）：worktree 隔离子代理的 cwd 是会话 workspace
// （worktree 目录），而完成最常见的只读浏览（view/grep/glob README 等）需要读
// 主仓库，主仓库位于 workspace 之外，过去每次都要 external_dir:admit 审批。
//
// 与 AllowedRoots（准入根：读写皆放行）刻意分开——只读根仅豁免**读**能力，
// 由 policy 的外部目录门按 CapWriteFS 判定；写路径仍受 worktree 限制。

const readOnlyRootsKey contextKey = "tool_read_only_roots"

// WithReadOnlyRoots stores filesystem roots whose *reads* are exempt from the
// external-directory gate for this run. Writes under them still go through the
// gate. Empty entries are dropped; an empty set leaves ctx unchanged.
func WithReadOnlyRoots(ctx context.Context, roots []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	cleaned := make([]string, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		cleaned = append(cleaned, trimmed)
	}
	if len(cleaned) == 0 {
		return ctx
	}
	return context.WithValue(ctx, readOnlyRootsKey, cleaned)
}

// ReadOnlyRoots returns the run's read-only exempt roots in insertion order. A
// missing set returns nil. Callers must not mutate the result.
func ReadOnlyRoots(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	value, ok := ctx.Value(readOnlyRootsKey).([]string)
	if !ok || len(value) == 0 {
		return nil
	}
	return append([]string(nil), value...)
}
