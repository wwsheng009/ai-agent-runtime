package types

import "testing"

// TestFSOwnerFromMetadata 锁定 P1-4 的归属解析：缺省/空值是 runtime（历史行为），
// 已知远端归属原样返回，未知取值回退 opaque（拼写错误不能放大本机探测）。
func TestFSOwnerFromMetadata(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]interface{}
		want     string
	}{
		{"nil metadata defaults to runtime", nil, ToolFSOwnerRuntime},
		{"missing key defaults to runtime", map[string]interface{}{"retry_class": "safe"}, ToolFSOwnerRuntime},
		{"explicit runtime", map[string]interface{}{ToolMetadataFSOwnerKey: "runtime"}, ToolFSOwnerRuntime},
		{"tool server", map[string]interface{}{ToolMetadataFSOwnerKey: "tool_server"}, ToolFSOwnerToolServer},
		{"browser mixed case", map[string]interface{}{ToolMetadataFSOwnerKey: "Browser"}, ToolFSOwnerBrowser},
		{"opaque", map[string]interface{}{ToolMetadataFSOwnerKey: "opaque"}, ToolFSOwnerOpaque},
		{"unknown falls back to opaque", map[string]interface{}{ToolMetadataFSOwnerKey: "toolserver"}, ToolFSOwnerOpaque},
		{"non-string falls back to runtime", map[string]interface{}{ToolMetadataFSOwnerKey: 7}, ToolFSOwnerRuntime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FSOwnerFromMetadata(tc.metadata); got != tc.want {
				t.Fatalf("owner=%q want %q", got, tc.want)
			}
		})
	}
}

// TestPathRoleFromMetadata 锁定 P1-4 的角色解析：大小写不敏感、未声明与声明但
// 取值不识别要区分开（后者必须被当成非 input 保守处理）。
func TestPathRoleFromMetadata(t *testing.T) {
	metadata := map[string]interface{}{
		ToolMetadataPathRolesKey: map[string]interface{}{
			"file_path": "input",
			"out.csv":   "output",
			"plan":      "inout",
			"cwd":       "workdir",
			"weird":     "readonly",
			"broken":    12,
		},
	}

	if role, declared := PathRoleFromMetadata(metadata, "FILE_PATH"); !declared || role != ToolPathRoleInput {
		t.Fatalf("file_path role=%q declared=%v", role, declared)
	}
	if role, declared := PathRoleFromMetadata(metadata, "out.csv"); !declared || role != ToolPathRoleOutput {
		t.Fatalf("out.csv role=%q declared=%v", role, declared)
	}
	if role, declared := PathRoleFromMetadata(metadata, "plan"); !declared || role != ToolPathRoleInOut {
		t.Fatalf("plan role=%q declared=%v", role, declared)
	}
	if role, declared := PathRoleFromMetadata(metadata, "cwd"); !declared || role != ToolPathRoleWorkdir {
		t.Fatalf("cwd role=%q declared=%v", role, declared)
	}
	if role, declared := PathRoleFromMetadata(metadata, "weird"); !declared || role != "" {
		t.Fatalf("unrecognized role must be declared-with-empty-role, got %q declared=%v", role, declared)
	}
	if role, declared := PathRoleFromMetadata(metadata, "broken"); !declared || role != "" {
		t.Fatalf("non-string role must be declared-with-empty-role, got %q declared=%v", role, declared)
	}
	if role, declared := PathRoleFromMetadata(metadata, "unknown_key"); declared || role != "" {
		t.Fatalf("undeclared key must report declared=false, got %q declared=%v", role, declared)
	}
	if role, declared := PathRoleFromMetadata(nil, "file_path"); declared || role != "" {
		t.Fatalf("nil metadata must report declared=false, got %q declared=%v", role, declared)
	}
}
