package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// TestKnowledgeHumanBytes 钉住字节格式化口径（状态面展示用）。
func TestKnowledgeHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{247 * 1024 * 1024, "247.0 MiB"},
	}
	for _, tc := range cases {
		if got := knowledgeHumanBytes(tc.in); got != tc.want {
			t.Errorf("knowledgeHumanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPrintKnowledgeStatusReport_TextSurface 钉住文本面必须出现的字段：
// 这些字段就是 06 §4 交付 5 的验收口径（索引状态/文件数/符号数/DB 大小/最近 job/锁等待 p95）。
func TestPrintKnowledgeStatusReport_TextSurface(t *testing.T) {
	report := knowledge.StatusReport{
		Mode:          knowledge.ModeShadow,
		Enabled:       true,
		Role:          knowledge.RoleOwner,
		OwnerPID:      4321,
		Workspace:     `E:\ws`,
		DBPath:        `E:\ws\.knowledge\knowledge.db`,
		DBSizeBytes:   1536,
		SchemaVersion: 1,
		Files:         12,
		Symbols:       34,
		Refs:          56,
		IndexedAt:     1700000000000,
		StalenessMS:   65000,
		LastJob: &knowledge.IndexJob{
			Kind: knowledge.IndexJobKindLight, Status: knowledge.IndexJobStatusDone,
			FilesTotal: 12, FilesDone: 12, DurationMS: 146900, StartedAt: 1699999000000,
		},
		LockWait: knowledge.LockWaitStats{Samples: 3, P50MS: 1.5, P95MS: 7, MaxMS: 9, RetryFailures: 1},
		DegradedReason: "read-only: store is owned by pid 999",
		GeneratedAt:    1700000065000,
	}
	var buf bytes.Buffer
	printKnowledgeStatusReport(&buf, report)
	out := buf.String()
	for _, want := range []string{
		"shadow",
		"owner (pid 4321)",
		`E:\ws`,
		"1.5 KiB",
		"files=12 symbols=34 refs=56",
		"light done 12/12 files",
		"p95=7.0ms",
		"retry_failures=1",
		"read-only: store is owned by pid 999",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status text missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestPrintKnowledgeStatusReport_OffHint 钉住 mode=off 的显式提示：
// 状态面最怕静默——off 必须被解释，而不是打印一排零让人猜。
func TestPrintKnowledgeStatusReport_OffHint(t *testing.T) {
	var buf bytes.Buffer
	printKnowledgeStatusReport(&buf, knowledge.StatusReport{Mode: knowledge.ModeOff, Role: knowledge.RoleNone})
	out := buf.String()
	if !strings.Contains(out, "off") || !strings.Contains(out, "knowledge.mode=shadow") {
		t.Errorf("off report must carry an explicit hint, got:\n%s", out)
	}
}

// TestNewKnowledgeCommand_StatusFlags 钉住 `knowledge status` 的 CLI 契约：
// 子命令存在、三个 flag 齐备（workspace / json / timeout）。
func TestNewKnowledgeCommand_StatusFlags(t *testing.T) {
	cmd := NewKnowledgeCommand(func() *config.Config { return nil })
	var status *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "status" {
			status = sub
			break
		}
	}
	if status == nil {
		t.Fatalf("knowledge status subcommand missing; got %v", cmd.Commands())
	}
	for _, flag := range []string{"workspace", "json", "timeout"} {
		if status.Flags().Lookup(flag) == nil {
			t.Errorf("knowledge status missing --%s flag", flag)
		}
	}
}
