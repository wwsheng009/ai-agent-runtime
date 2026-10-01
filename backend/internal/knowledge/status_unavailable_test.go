package knowledge

import (
	"strings"
	"testing"
)

// TestUnavailableStatusReportsConfiguredMode 钉住"配置开了但不可用"的状态口径
// （Phase 5 E2E 登记②）：mode 报告配置意图、enabled=false、degraded_reason 带
// 工具面同款 token 前缀（index_unavailable）并保留原始原因（含迁移指引）。
func TestUnavailableStatusReportsConfiguredMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeOn
	cfg.Workspace = `E:\ws`
	report := UnavailableStatus(cfg, "knowledge: store schema v3 is older than this binary (v4); open it once as writer to migrate")

	if report.Mode != ModeOn {
		t.Fatalf("mode = %q, want on（报告配置意图）", report.Mode)
	}
	if report.Enabled {
		t.Fatal("不可用时 enabled 必须为 false")
	}
	if report.Role != RoleNone {
		t.Fatalf("role = %q, want none", report.Role)
	}
	if !strings.HasPrefix(report.DegradedReason, "index_unavailable: ") {
		t.Fatalf("degraded_reason = %q, want index_unavailable 前缀（与工具面同 token）", report.DegradedReason)
	}
	if !strings.Contains(report.DegradedReason, "open it once as writer to migrate") {
		t.Fatalf("degraded_reason 必须保留原始原因：%q", report.DegradedReason)
	}
	if report.DBPath == "" || !strings.HasSuffix(report.DBPath, "knowledge.db") {
		t.Fatalf("db_path = %q, want 指向 knowledge.db（供排障定位落点）", report.DBPath)
	}
	if report.Workspace != `E:\ws` {
		t.Fatalf("workspace = %q, want E:\\ws", report.Workspace)
	}
	if report.GeneratedAt <= 0 {
		t.Fatal("generated_at 必须填充")
	}
}

// TestUnavailableStatusOffStaysMinimal 钉住 off 的最小载荷：不编造 db_path 与
// degraded_reason（"off 是合法状态"与"配置了但打不开"必须能从状态面区分）。
func TestUnavailableStatusOffStaysMinimal(t *testing.T) {
	report := UnavailableStatus(DefaultConfig(), "")
	if report.Mode != ModeOff || report.Enabled {
		t.Fatalf("off 载荷 mode=%q enabled=%v, want off/false", report.Mode, report.Enabled)
	}
	if report.DBPath != "" || report.DegradedReason != "" || report.Workspace != "" {
		t.Fatalf("off 载荷必须最小化: %+v", report)
	}
}
