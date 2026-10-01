package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// TestChatWebKnowledgeStatusReportsActivationFailure 钉住 Phase 5 E2E 登记②：
// 配置开了但接入失败（Knowledge=nil 且 KnowledgeError 非空）时，状态面必须
// 报"已配置但不可用"（mode=配置值 + degraded_reason），而不是退化成 mode=off。
func TestChatWebKnowledgeStatusReportsActivationFailure(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "runtime.yaml")
	if err := os.WriteFile(cfgPath, []byte("knowledge:\n  mode: on\n  code_tools: on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{
		Config:            &config.Config{},
		RuntimeConfigPath: cfgPath,
		KnowledgeError:    "knowledge: store schema v3 is older than this binary (v4); open it once as writer to migrate",
	}
	report, err := chatWebKnowledgeStatusReportFor(context.Background(), session)
	if err != nil {
		t.Fatalf("report err = %v", err)
	}
	if report.Mode != knowledge.ModeOn || report.Enabled {
		t.Fatalf("mode=%q enabled=%v, want on/false", report.Mode, report.Enabled)
	}
	if !strings.Contains(report.DegradedReason, "index_unavailable") ||
		!strings.Contains(report.DegradedReason, "open it once as writer to migrate") {
		t.Fatalf("degraded_reason = %q, want 含 index_unavailable 与原始原因", report.DegradedReason)
	}
}

// TestChatWebKnowledgeStatusNilStaysOff 钉住未失败路径：无 Knowledge、无
// KnowledgeError 时保持 mode=off 最小载荷（与旧行为逐字节一致）。
func TestChatWebKnowledgeStatusNilStaysOff(t *testing.T) {
	report, err := chatWebKnowledgeStatusReportFor(context.Background(), &ChatSession{})
	if err != nil {
		t.Fatalf("report err = %v", err)
	}
	if report.Mode != knowledge.ModeOff || report.DegradedReason != "" || report.Enabled {
		t.Fatalf("payload = %+v, want 最小 off", report)
	}
}
