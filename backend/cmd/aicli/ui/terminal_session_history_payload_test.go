package ui

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestTerminalHistoryPayloadToWriteRequiresResidentProvenance pins the resident
// tail trim proof: leading payload rows may only be dropped when the payload
// continues a cell the region already owns. Text equality alone is not
// re-delivery proof, because two different cells can render identical lines.
func TestTerminalHistoryPayloadToWriteRequiresResidentProvenance(t *testing.T) {
	resident := []string{"alpha", "beta"}
	payload := []string{"alpha", "beta", "gamma"}
	residentCells := map[uint64]struct{}{7: {}}

	cases := []struct {
		name      string
		cells     map[uint64]struct{}
		delivered []HistoryCommit
		inserted  []string
		want      []string
	}{
		{
			name:      "resident cell re-delivery trims the resident prefix",
			cells:     residentCells,
			delivered: []HistoryCommit{{CellID: scene.CellID(7)}},
			inserted:  payload,
			want:      []string{"gamma"},
		},
		{
			name:      "new cell with identical text is written in full",
			cells:     residentCells,
			delivered: []HistoryCommit{{CellID: scene.CellID(8)}},
			inserted:  payload,
			want:      payload,
		},
		{
			name:      "fresh region without ownership proof is written in full",
			cells:     nil,
			delivered: []HistoryCommit{{CellID: scene.CellID(7)}},
			inserted:  payload,
			want:      payload,
		},
		{
			name:      "incremental payload shorter than the tail is never trimmed",
			cells:     residentCells,
			delivered: []HistoryCommit{{CellID: scene.CellID(7)}},
			inserted:  []string{"gamma"},
			want:      []string{"gamma"},
		},
		{
			name:      "empty delivery keeps the payload intact",
			cells:     residentCells,
			delivered: nil,
			inserted:  payload,
			want:      payload,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := terminalHistoryPayloadToWrite(resident, tc.cells, tc.delivered, tc.inserted)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("terminalHistoryPayloadToWrite() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTerminalAppendHistoryTailCellsRecordsSemanticOwners(t *testing.T) {
	first := terminalAppendHistoryTailCells(nil, []HistoryCommit{{CellID: scene.CellID(3)}})
	if len(first) != 1 {
		t.Fatalf("after first append len(cells) = %d, want 1", len(first))
	}
	second := terminalAppendHistoryTailCells(first, []HistoryCommit{
		{CellID: scene.CellID(4)},
		{CellID: scene.CellID(3)},
	})
	if len(second) != 2 {
		t.Fatalf("after second append len(cells) = %d, want 2", len(second))
	}
	for _, id := range []uint64{3, 4} {
		if _, ok := second[id]; !ok {
			t.Fatalf("cell %d missing from recorded owners %v", id, second)
		}
	}
	if got := terminalAppendHistoryTailCells(second, nil); len(got) != 2 {
		t.Fatalf("empty delivery must keep owners unchanged, got %v", got)
	}
}
