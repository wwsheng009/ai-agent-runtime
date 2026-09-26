package tools

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
)

func TestToolkitArgRepairNotesReportsPromotions(t *testing.T) {
	raw := map[string]interface{}{
		"cmd": "go test ./...",
		"cwd": "backend",
	}
	normalized := normalizeToolkitToolArgs("bash", raw)
	notes := toolkitArgRepairNotes("bash", raw, normalized)
	if len(notes) != 2 {
		t.Fatalf("expected 2 alias notes, got %#v", notes)
	}
	joined := strings.Join(notes, ",")
	if !strings.Contains(joined, "alias:cmd→command") || !strings.Contains(joined, "alias:cwd→workdir") {
		t.Fatalf("unexpected notes: %v", notes)
	}
	for _, note := range notes {
		if strings.Contains(note, "go test") || strings.Contains(note, "backend") {
			t.Fatalf("repair notes must not leak values: %v", notes)
		}
	}
}

func TestToolkitArgRepairNotesSilentWhenCanonicalKeysUsed(t *testing.T) {
	raw := map[string]interface{}{"command": "go version", "workdir": "backend"}
	normalized := normalizeToolkitToolArgs("bash", raw)
	if notes := toolkitArgRepairNotes("bash", raw, normalized); len(notes) != 0 {
		t.Fatalf("expected no notes for canonical args, got %#v", notes)
	}
}

func TestToolkitArgRepairNotesReportsRawUnwrap(t *testing.T) {
	raw := map[string]interface{}{"_raw": `{"file_path":"README.md"}`}
	normalized := normalizeToolkitToolArgs("view", toolargs.Normalize(raw))
	notes := toolkitArgRepairNotes("view", raw, normalized)
	found := false
	for _, note := range notes {
		if note == "unwrap:_raw" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unwrap:_raw note, got %#v", notes)
	}
}
