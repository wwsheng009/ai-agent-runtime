package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// collectedTranscriptRows renders every queued transcript-origin commit in the
// order the planner produced it. A2 first cut: mutable streaming mints nothing,
// so finalize must plan the whole source from row zero.
func collectedTranscriptRows(t *testing.T, state UIControllerState) []string {
	t.Helper()
	rows := make([]string, 0)
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Origin != HistoryCommitTranscript || entry.State != HistoryCommitQueued {
			continue
		}
		for _, line := range entry.Commit.Lines {
			rows = append(rows, renderLineText(line))
		}
	}
	return rows
}

func assertEveryMarkerOnce(t *testing.T, rows []string, markers []string) {
	t.Helper()
	joined := strings.Join(rows, "\n")
	for _, marker := range markers {
		if count := strings.Count(joined, marker); count != 1 {
			t.Fatalf("marker %q planned %d times, want exactly once\n%s", marker, count, joined)
		}
	}
}

// A2 第一刀（停铸 active）：mutable 期间的 Set/Update 不再铸任何历史提交；
// finalize 从 source 0 一次性铸全量 transcript 提交，append-only 更新不产生
// 前缀重放。
func TestMutableActiveUpdatesMintNoHistoryUntilFinalize(t *testing.T) {
	lines := make([]string, 18)
	for index := range lines {
		lines[index] = fmt.Sprintf("epoch-row-%03d", index)
	}
	source := strings.Join(lines, "\n")
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, 2)
	state = reduceUIControllerState(state, SetActiveCellAction{Active: ActiveCellState{
		CellID: 91, Revision: 1, Kind: scene.KindAssistant,
		Phase: ActiveCellMutable, Source: source,
	}}, 3)

	if entries := state.HistoryEffects.Entries(); len(entries) != 0 {
		t.Fatalf("mutable Set minted history effects: %+v", entries)
	}
	if state.Active.Enqueued.End != 0 || state.Active.Acked.End != 0 {
		t.Fatalf("mutable Set moved delivery frontiers: %+v", state.Active)
	}

	nextSource := source + "\nepoch-row-018\nepoch-row-019"
	state = reduceUIControllerState(state, UpdateActiveCellAction{
		ExpectedCellID: 91, ExpectedRevision: 1,
		Active: ActiveCellState{
			CellID: 91, Revision: 2, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: nextSource,
			Stable: state.Active.Stable, Enqueued: state.Active.Enqueued, Acked: state.Active.Acked,
		},
	}, 4)
	if entries := state.HistoryEffects.Entries(); len(entries) != 0 {
		t.Fatalf("append-only mutable update minted history effects: %+v", entries)
	}

	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		Snapshot: &scene.Snapshot{Revision: 3, Cells: []*scene.TranscriptCell{{
			ID: 91, Revision: 3, Kind: scene.KindAssistant,
			Source: nextSource, Phase: scene.CellCommitted,
		}}},
		ExpectedActiveCellID: 91, ExpectedActiveRevision: 2,
		ExpectedSceneRevision: 3,
		ExpectedActiveKind:    scene.KindAssistant, ExpectedActiveKindKnown: true,
	}, 5)

	rows := collectedTranscriptRows(t, state)
	joined := strings.Join(rows, "\n")
	if joined == "" {
		t.Fatal("finalize planned no transcript delivery")
	}
	assertEveryMarkerOnce(t, rows, lines)
	if !strings.Contains(joined, "epoch-row-018") || !strings.Contains(joined, "epoch-row-019") {
		t.Fatalf("finalize plan omitted appended rows: %q", joined)
	}
}

// A2 第一刀：结构化（markdown/reasoning）mutable 源同样不铸提交；finalize 从 0
// 全量铸一次，渲染载荷不重放、不泄漏原始 markdown 语法。
func TestStructuredMutableSourceMintsNothingUntilFinalize(t *testing.T) {
	tests := []struct {
		name string
		kind scene.CellKind
		line func(int) string
	}{
		{
			name: "reasoning",
			kind: scene.KindReasoning,
			line: func(index int) string {
				return fmt.Sprintf("**reasoning-row-%03d**", index)
			},
		},
		{
			name: "assistant markdown",
			kind: scene.KindAssistant,
			line: func(index int) string {
				return fmt.Sprintf("**assistant-row-%03d**", index)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			makeSource := func(start, end int) string {
				lines := make([]string, 0, end-start)
				for index := start; index < end; index++ {
					lines = append(lines, test.line(index))
				}
				return strings.Join(lines, "\n")
			}
			source := makeSource(0, 30)

			const cellID = scene.CellID(91)
			state := UIControllerState{}
			state = reduceUIControllerState(state, Resize{Width: 80, Height: 12, Generation: 1}, 1)
			state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, 2)
			state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
				Revision:       1,
				ContentVersion: 1,
				Cells: []*scene.TranscriptCell{{
					ID: cellID, Sequence: 1, Revision: 1, Kind: test.kind,
					Source: source, Phase: scene.CellMutable,
				}},
			}}, 3)

			if entries := state.HistoryEffects.Entries(); len(entries) != 0 {
				t.Fatalf("structured mutable source minted history effects: %+v", entries)
			}

			state = reduceUIControllerState(state, FinalizeActiveCellAction{
				Snapshot: &scene.Snapshot{
					Revision:       2,
					ContentVersion: 2,
					Cells: []*scene.TranscriptCell{{
						ID: cellID, Sequence: 1, Revision: 2, Kind: test.kind,
						Source: source, Phase: scene.CellCommitted,
					}},
				},
				ExpectedActiveCellID: cellID, ExpectedActiveRevision: 1,
				ExpectedSceneRevision: 2,
				ExpectedActiveKind:    test.kind, ExpectedActiveKindKnown: true,
			}, 4)

			rows := collectedTranscriptRows(t, state)
			joined := strings.Join(rows, "\n")
			if !strings.Contains(joined, "row-000") || !strings.Contains(joined, "row-029") {
				t.Fatalf("finalize did not plan the whole structured source: %q", joined)
			}
			if count := strings.Count(joined, "row-000"); count != 1 {
				t.Fatalf("structured finalize replayed the prefix %d times: %q", count, joined)
			}
			// reasoning 的 source-faithful 渲染（保留 ** 原文 + divider）由
			// TestPlanMutableReasoningHistoryCommitMatchesSourceFaithfulFinalize
			// 单独钉住；这里只验证"全量一次、无重放"。
		})
	}
}

// A2 第一刀：mutable 溢出（含 reasoning 前缀）不再产生 active 提交；finalize
// 从 source 0 铸全量，每个标记恰好一次。
func TestFinalizeActiveCellPlansWholeSourceFromZero(t *testing.T) {
	const width, height = 100, 24
	markers := make([]string, 40)
	for index := range markers {
		markers[index] = fmt.Sprintf("FINALIZED-SUFFIX-%02d terminal history validation", index+1)
	}
	source := "Terminal scrollback keeps completed rows in the host buffer.\n\n" +
		"FINALIZED-SUFFIX-REASONING\n\n" + strings.Join(markers, "\n")

	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: width, Height: height, Generation: 1}, 1)
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, 2)
	state = reduceUIControllerState(state, SetActiveCellAction{Active: ActiveCellState{
		CellID: 71, Revision: 40, Kind: scene.KindAssistant,
		Phase: ActiveCellMutable, Source: source,
	}}, 3)

	if entries := state.HistoryEffects.Entries(); len(entries) != 0 {
		t.Fatalf("mutable overflow minted active entries: %d", len(entries))
	}

	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		Snapshot: &scene.Snapshot{Revision: 41, Cells: []*scene.TranscriptCell{{
			ID: 71, Revision: 41, Kind: scene.KindAssistant,
			Source: source, Phase: scene.CellCommitted,
		}}},
		ExpectedActiveCellID: 71, ExpectedActiveRevision: 40,
		ExpectedSceneRevision: 41,
		ExpectedActiveKind:    scene.KindAssistant, ExpectedActiveKindKnown: true,
	}, 100)

	rows := collectedTranscriptRows(t, state)
	assertEveryMarkerOnce(t, rows, markers)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "Terminal scrollback keeps completed") ||
		!strings.Contains(joined, "FINALIZED-SUFFIX-REASONING") {
		t.Fatalf("finalize plan omitted leading source rows: %q", joined)
	}
}

// A2 第一刀：mutable 期间无在途 active 批次，finalize 立即全量规划，不再需要
// 延迟所有权转移与续跑（旧 guard 保留为死代码，第二刀删除）。
func TestFinalizeActiveCellPlansWholeSourceWithoutDeferral(t *testing.T) {
	const width, height = 100, 24
	markers := make([]string, 40)
	for index := range markers {
		markers[index] = fmt.Sprintf("DEFER-FINALIZE-%02d terminal history validation", index+1)
	}
	source := strings.Join(markers, "\n")

	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: width, Height: height, Generation: 1}, 1)
	state = reduceUIControllerState(state, SetSemanticActiveCellProjectionAction{Enabled: true}, 2)
	state = reduceUIControllerState(state, SetActiveCellAction{Active: ActiveCellState{
		CellID: 81, Revision: 40, Kind: scene.KindAssistant,
		Phase: ActiveCellMutable, Source: source,
	}}, 3)
	if entries := state.HistoryEffects.Entries(); len(entries) != 0 {
		t.Fatalf("mutable source minted active entries: %d", len(entries))
	}

	state = reduceUIControllerState(state, FinalizeActiveCellAction{
		Snapshot: &scene.Snapshot{Revision: 41, Cells: []*scene.TranscriptCell{{
			ID: 81, Revision: 41, Kind: scene.KindAssistant,
			Source: source, Phase: scene.CellCommitted,
		}}},
		ExpectedActiveCellID: 81, ExpectedActiveRevision: 40,
		ExpectedSceneRevision: 41,
		ExpectedActiveKind:    scene.KindAssistant, ExpectedActiveKindKnown: true,
	}, 4)

	rows := collectedTranscriptRows(t, state)
	assertEveryMarkerOnce(t, rows, markers)
	if state.HistoryEffects.PlanIncomplete {
		t.Fatalf("finalize left continuation armed: %#v", state.HistoryEffects)
	}
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Origin != HistoryCommitTranscript {
			t.Fatalf("finalize minted non-transcript entry: %#v", entry)
		}
	}
}
