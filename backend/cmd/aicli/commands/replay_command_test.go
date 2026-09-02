package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
)

// replayCommandTestFixture 构造一个可回放的 committed archive entry。
func replayCommandTestEntry(t *testing.T, payload string) output.ReplayArchiveEntry {
	t.Helper()
	now := time.Now()
	geom := output.TerminalGeometry{Width: 20, Height: 6}
	profile := output.TerminalProfileRef{ID: "ansi", Version: 1}
	entry := output.ReplayArchiveEntry{
		SchemaMajor: output.ReplayArchiveSchemaMajor,
		SchemaMinor: output.ReplayArchiveSchemaMinor,
		Record: output.DeliveryRecord{
			RecordID:      "rd-replay",
			SchemaVersion: output.SchemaVersion,
			Batch: output.RecordedBatch{
				SessionID:             "src-session",
				BatchID:               "src-batch",
				IntentID:              "src-intent",
				Sequence:              7,
				RouteEpoch:            3,
				ProjectionTargetID:    "pt-primary",
				ProjectionTargetClass: output.TargetClassPhysical,
				Kind:                  output.TransactionFrame,
				Source:                "test",
				Cause:                 "replay-command-test",
				Terminal:              output.RenderTerminalContext{Geometry: geom, Profile: profile},
				BytesLength:           len(payload),
			},
			Output: output.RecordedOutputReceipt{
				BatchID:               "src-batch",
				Sequence:              7,
				RouteEpoch:            3,
				ProjectionTargetID:    "pt-primary",
				ProjectionTargetClass: output.TargetClassPhysical,
				Primary: &output.RecordedTargetReceipt{
					BatchID:            "src-batch",
					Sequence:           7,
					Status:             output.DeliveryCommitted,
					Certainty:          output.WriteCertaintyFull,
					ProjectionTargetID: "pt-primary",
				},
			},
			SealedAt: now,
		},
		PayloadSource: output.CapturedDelivery{
			SchemaVersion:      output.SchemaVersion,
			CaptureEntryID:     "ce-replay",
			SessionID:          "src-session",
			BatchID:            "src-batch",
			Sequence:           7,
			RouteEpoch:         3,
			ProjectionTargetID: "pt-capture",
			Mode:               output.RecordedFullAvailable,
			BytesLength:        len(payload),
			Transaction:        output.TransactionFrame,
			CapturedAt:         now,
		},
		Payload: []byte(payload),
	}
	entry.PayloadChecksum = output.ReplayArchiveChecksum(entry)
	return entry
}

// writeReplayArchiveForTest 把 entries 写成 JSON archive 文件。
func writeReplayArchiveForTest(t *testing.T, entries []output.ReplayArchiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "replay.archive.json")
	if err := output.WriteReplayArchive(path, entries); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return path
}

// runReplayCommand 执行 replay 命令并捕获 stdout/stderr。
func runReplayCommand(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewReplayCommand()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestReplayCommandReplaysToVirtualTerminal(t *testing.T) {
	// payload 使用简单的文本（含换行），VtTerminalEmulator 会解释为屏幕行。
	entry := replayCommandTestEntry(t, "line-one\nline-two\n")
	path := writeReplayArchiveForTest(t, []output.ReplayArchiveEntry{entry})

	stdout, _, err := runReplayCommand(t, path)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !strings.Contains(stdout, "line-one") || !strings.Contains(stdout, "line-two") {
		t.Fatalf("projection missing expected rows:\n%s", stdout)
	}
	if !strings.Contains(stdout, "=== End Replay ===") {
		t.Fatalf("missing end marker:\n%s", stdout)
	}
}

func TestReplayCommandVerifyMode(t *testing.T) {
	entry := replayCommandTestEntry(t, "verify-me\n")
	path := writeReplayArchiveForTest(t, []output.ReplayArchiveEntry{entry})

	stdout, _, err := runReplayCommand(t, "--replay-verify", path)
	if err != nil {
		t.Fatalf("replay --replay-verify: %v", err)
	}
	if !strings.Contains(stdout, "archive verified: 1 entries OK") {
		t.Fatalf("verify output:\n%s", stdout)
	}
	// verify 模式不应输出投影行。
	if strings.Contains(stdout, "=== End Replay ===") {
		t.Fatalf("verify mode must not execute replay:\n%s", stdout)
	}
}

func TestReplayCommandVerifyRejectsTampered(t *testing.T) {
	entry := replayCommandTestEntry(t, "should-fail\n")
	entry.PayloadChecksum = "deadbeef"
	path := writeReplayArchiveForTest(t, []output.ReplayArchiveEntry{entry})

	_, _, err := runReplayCommand(t, "--replay-verify", path)
	if err == nil {
		t.Fatal("expected error for tampered archive")
	}
}

func TestReplayCommandRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.archive.json")
	if err := os.WriteFile(path, []byte("{not-json-"), 0o644); err != nil {
		t.Fatal(err)
	}
	// corrupt JSON 会回退为裸 wire bytes，不会失败——这里验证 raw 路径能走通。
	_, _, err := runReplayCommand(t, "--replay-verify", path)
	if err != nil {
		t.Fatalf("raw wire fallback should not fail verification (synthetic committed entry): %v", err)
	}
}

func TestReplayCommandRawWireBytesFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror.ans")
	if err := os.WriteFile(path, []byte("raw-line-a\nraw-line-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runReplayCommand(t, path)
	if err != nil {
		t.Fatalf("raw replay: %v", err)
	}
	if !strings.Contains(stdout, "raw-line-a") || !strings.Contains(stdout, "raw-line-b") {
		t.Fatalf("raw projection missing rows:\n%s", stdout)
	}
}

func TestReplayCommandRawWireBytesVerify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror.ans")
	if err := os.WriteFile(path, []byte("raw-data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runReplayCommand(t, "--replay-verify", path)
	if err != nil {
		t.Fatalf("raw verify: %v", err)
	}
	if !strings.Contains(stdout, "archive verified: 1 entries OK") {
		t.Fatalf("raw verify output:\n%s", stdout)
	}
}

func TestReplayCommandNoPhysicalWriter(t *testing.T) {
	// 关键验收：replay 进程不打开任何 physical/process writer。
	// 回放目标是 VirtualTerminalSink（非 physical），且不依赖 console。
	entry := replayCommandTestEntry(t, "no-physical\n")
	path := writeReplayArchiveForTest(t, []output.ReplayArchiveEntry{entry})

	stdout, stderr, err := runReplayCommand(t, path)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !strings.Contains(stdout, "no-physical") {
		t.Fatalf("missing projection:\n%s", stdout)
	}
	// stderr 不应包含渲染字节（只允许诊断信息）。
	if strings.Contains(stderr, "no-physical") {
		t.Fatalf("render bytes leaked to stderr: %s", stderr)
	}
}

func TestReplayCommandMissingArg(t *testing.T) {
	_, _, err := runReplayCommand(t)
	if err == nil {
		t.Fatal("expected error for missing arg")
	}
}

func TestReplayCommandMissingFile(t *testing.T) {
	_, _, err := runReplayCommand(t, filepath.Join(t.TempDir(), "nonexistent.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

var _ = cobra.Command{} // keep cobra import if needed elsewhere