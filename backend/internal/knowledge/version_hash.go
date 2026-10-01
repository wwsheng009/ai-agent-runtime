package knowledge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// WorkspaceVersion 计算工作区的知识版本（06 §4 Phase 2 W1 的缺口补齐）。
//
// 版本 = 稳定哈希(工作区 id, 全部未软删除文件的 path+content_hash 集合,
// AdapterVersion, schema 版本)。语义：
//   - 任一文件内容变化（content_hash 变化）、文件增删、adapter 升级或
//     迁移前进都会改变版本；
//   - 同一输入必然得到同一字符串（跨进程、跨平台稳定，path 统一为 '/')；
//   - 与具体时间/索引运行顺序无关，因此可用于跨任务的 stale 判定
//     （W3：版本不匹配的探索记忆直接不可用）与 Phase 5 的版本向量。
//
// 返回的字符串是不透明 token（当前形如 "wv1_<hex>"），调用方只做相等比较，
// 不得解析其内部结构。
func WorkspaceVersion(ctx context.Context, store Store, workspaceID string) (string, error) {
	if store == nil {
		return "", errors.New("knowledge: workspace version: store is required")
	}
	if strings.TrimSpace(workspaceID) == "" {
		return "", errors.New("knowledge: workspace version: workspace_id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	files, err := store.ListActiveFiles(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("knowledge: workspace version: %w", err)
	}
	schemaVersion, err := store.SchemaVersion(ctx)
	if err != nil {
		return "", fmt.Errorf("knowledge: workspace version: %w", err)
	}
	// Phase 4：adapter 版本以库内记录为准（可能不是 builtin）；未记录时回落
	// 包级常量，使既有库的版本值与 Phase 4 之前逐字节一致。
	adapterVersion, err := store.WorkspaceAdapterVersion(ctx, workspaceID)
	if err != nil || strings.TrimSpace(adapterVersion) == "" {
		adapterVersion = AdapterVersion
	}
	return workspaceVersionHash(workspaceID, adapterVersion, schemaVersion, files), nil
}

// workspaceVersionHash 是 WorkspaceVersion 的纯函数内核：把参与版本的分量
// 排序、拼接后求摘要。
//
// adapter/schema 以参数传入而不是直接读包级常量，使"版本对 adapter 升级 /
// 迁移前进敏感"这一性质可以被单测直接钉住（无需真的换常量）。
func workspaceVersionHash(workspaceID, adapterVersion string, schemaVersion int, files []FileRecord) string {
	entries := make([]string, 0, len(files))
	for _, file := range files {
		entries = append(entries, normalizeRelPath(file.Path)+"="+file.ContentHash)
	}
	sort.Strings(entries)

	parts := make([]string, 0, len(entries)+4)
	parts = append(parts, "wv1", workspaceID, adapterVersion, strconv.Itoa(schemaVersion))
	parts = append(parts, entries...)
	return "wv1_" + digest(parts...)
}
