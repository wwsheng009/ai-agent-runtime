package connectors

import "testing"

// TestBuildHostID 锁定实例唯一 HostID 组装：instance 空 = 裸 kind（历史值）。
func TestBuildHostID(t *testing.T) {
	cases := []struct{ kind, instance, want string }{
		{"aicli", "node-abc", "aicli:node-abc"},
		{"aicli", "", "aicli"},
		{"runtime-server", "", "runtime-server"},
		{"gateway", "gw-1", "gateway:gw-1"},
		{"  aicli  ", "  node-1  ", "aicli:node-1"},
		{"", "x", ""}, // 无 kind 不产出畸形 id
	}
	for _, tc := range cases {
		if got := BuildHostID(tc.kind, tc.instance); got != tc.want {
			t.Fatalf("BuildHostID(%q,%q) = %q, want %q", tc.kind, tc.instance, got, tc.want)
		}
	}
}

// TestHostKindOf 锁定 kind 提取：`kind:instance` / 裸词 / 空。
func TestHostKindOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"aicli:node-abc", "aicli"},
		{"runtime-server", "runtime-server"},
		{"", ""},
		{"aicli:", "aicli"},
	}
	for _, tc := range cases {
		if got := HostKindOf(tc.in); got != tc.want {
			t.Fatalf("HostKindOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHostMatchesPolicy 锁定 policy.runtime_owner 准入语义（切片 A 的兼容性
// 核心）：裸词按 HostKind 匹配——HostID 实例化后存量配置仍命中。
func TestHostMatchesPolicy(t *testing.T) {
	cases := []struct {
		hostID, policyOwner string
		want                bool
	}{
		{"aicli:node-1", "aicli", true},          // 实例化 HostID 命中存量裸词配置
		{"aicli:node-1", "AICLI", true},          // 大小写不敏感
		{"aicli", "aicli", true},                 // 裸 HostID（历史形态）
		{"aicli:node-1", "runtime-server", false},
		{"aicli:node-1", "gateway", false},
		{"runtime-server", "runtime-server", true},
		{"aicli:node-1", "auto", true},  // auto = 任何宿主
		{"aicli:node-1", "", true},      // 空 = 任何宿主
		{"aicli:node-1", "  ", true},    // 空白 = 任何宿主
		{"gateway:gw-1", "gateway", true},
	}
	for _, tc := range cases {
		if got := HostMatchesPolicy(tc.hostID, tc.policyOwner); got != tc.want {
			t.Fatalf("HostMatchesPolicy(%q,%q) = %v, want %v", tc.hostID, tc.policyOwner, got, tc.want)
		}
	}
}
