package supervision

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// P0-4 改动 2：read_agent_result 的有界结构化输出。
func TestBuildReadResultPayloadSections(t *testing.T) {
	record := sampleAgentResultRecord()

	full := BuildReadResultPayload(record, ReadResultArgs{SessionID: "child-1"})
	require.Equal(t, ResultSourceTaskResult, full.Source)
	require.Equal(t, "failed", full.Status)
	require.NotEmpty(t, full.Summary)
	require.Len(t, full.Findings, MaxReadResultFindings)
	require.Len(t, full.Changes, MaxReadResultChanges)
	require.Len(t, full.Errors, MaxReadResultErrors)
	require.NotNil(t, full.Usage)
	require.True(t, full.Truncated, "count caps must flag truncation")

	onlySummary := BuildReadResultPayload(record, ReadResultArgs{
		SessionID: "child-1",
		Sections:  []string{ReadResultSectionSummary},
	})
	require.NotEmpty(t, onlySummary.Summary)
	require.Empty(t, onlySummary.Findings)
	require.Empty(t, onlySummary.Changes)
	require.Empty(t, onlySummary.Errors)
	require.Nil(t, onlySummary.Usage)

	raw, err := json.Marshal(onlySummary)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"findings"`)
}

func TestBuildReadResultPayloadMaxCharsBudget(t *testing.T) {
	record := sampleAgentResultRecord()
	payload := BuildReadResultPayload(record, ReadResultArgs{
		SessionID: "child-1",
		MaxChars:  400,
	})
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.LessOrEqual(t, utf8.RuneCount(raw), 400, "the serialized payload must respect max_chars")
	require.True(t, payload.Truncated)
	require.NotEmpty(t, payload.Status, "the structured skeleton is never dropped")
	require.Equal(t, ResultSourceTaskResult, payload.Source)
}

func TestBuildReadResultPayloadUsageAndIdentity(t *testing.T) {
	finished := time.Date(2026, 9, 17, 11, 30, 0, 0, time.UTC)
	record := AgentResultRecord{
		Source:     ResultSourceCompletionPayload,
		SessionID:  "child-9",
		Status:     "succeeded",
		Summary:    "done",
		Usage:      AgentResultUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
		FinishedAt: &finished,
	}
	payload := BuildReadResultPayload(record, ReadResultArgs{})
	require.Equal(t, "child-9", payload.SessionID)
	require.Equal(t, ResultSourceCompletionPayload, payload.Source)
	require.NotNil(t, payload.Usage)
	require.Equal(t, 30, payload.Usage.TotalTokens)
	require.NotNil(t, payload.FinishedAt)
	require.False(t, payload.Truncated)
}

func TestNormalizeReadResultSectionsRejectsUnknown(t *testing.T) {
	sections, err := NormalizeReadResultSections([]string{"Summary", "usage", "summary"})
	require.NoError(t, err)
	require.Equal(t, []string{ReadResultSectionSummary, ReadResultSectionUsage}, sections)

	_, err = NormalizeReadResultSections([]string{"everything"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported section")

	all, err := NormalizeReadResultSections(nil)
	require.NoError(t, err)
	require.Nil(t, all)
}

func TestNoResultRecordedPayloadIsActionable(t *testing.T) {
	payload := NoResultRecordedPayload(" child-1 ", " task-1 ")
	require.Equal(t, ResultSourceNone, payload.Source)
	require.Equal(t, "no_result_recorded", payload.ErrorCode)
	require.Equal(t, "child-1", payload.SessionID)
	require.Equal(t, "task-1", payload.TaskID)
	require.Contains(t, payload.NextAction, "read_agent_events")
	require.Contains(t, payload.NextAction, "wait_agent")

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"source":"none"`)
	require.Contains(t, string(raw), `"error_code":"no_result_recorded"`)
}

func TestResultBounds(t *testing.T) {
	summary, truncated := BoundResultSummary(strings.Repeat("s", MaxSnapshotResultSummaryRunes+5), false)
	require.True(t, truncated)
	require.Equal(t, MaxSnapshotResultSummaryRunes, len([]rune(summary)))

	summary, truncated = BoundResultSummary("short", false)
	require.False(t, truncated)
	require.Equal(t, "short", summary)

	refs := BoundArtifactRefs([]string{"", "a", "a", strings.Repeat("b", MaxSnapshotArtifactRefChars+1), "c", "d"})
	require.Len(t, refs, MaxSnapshotResultArtifactRefs)
	require.Equal(t, "a", refs[0])
	require.Equal(t, MaxSnapshotArtifactRefChars, len([]rune(refs[1])))

	require.Equal(t, DefaultReadResultMaxChars, ReadResultMaxChars(0))
	require.Equal(t, MinReadResultMaxChars, ReadResultMaxChars(1))
	require.Equal(t, MaxReadResultMaxChars, ReadResultMaxChars(1_000_000))
	require.Equal(t, 1234, ReadResultMaxChars(1234))
}

// sampleAgentResultRecord produces a record that exceeds every count cap so
// the truncation flags are exercised deterministically.
func sampleAgentResultRecord() AgentResultRecord {
	finished := time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)
	record := AgentResultRecord{
		Source:     ResultSourceTaskResult,
		TaskID:     "task-1",
		SessionID:  "child-1",
		Status:     "failed",
		Summary:    strings.Repeat("summary ", 80),
		Usage:      AgentResultUsage{TotalTokens: 42, ToolCalls: 3},
		FinishedAt: &finished,
	}
	for i := 0; i < MaxReadResultFindings+2; i++ {
		record.Findings = append(record.Findings, "finding summary")
	}
	for i := 0; i < MaxReadResultChanges+2; i++ {
		record.Changes = append(record.Changes, AgentResultChange{
			Path:         "backend/internal/x.go",
			Summary:      "change",
			Status:       "applied",
			ArtifactRefs: []string{"artifact://change"},
		})
	}
	for i := 0; i < MaxReadResultArtifacts+2; i++ {
		record.Artifacts = append(record.Artifacts, "artifact://ref")
	}
	for i := 0; i < MaxReadResultErrors+2; i++ {
		record.Errors = append(record.Errors, AgentResultError{Code: "TOOL_TIMEOUT", Message: "timed out", Retryable: true})
	}
	return record
}
