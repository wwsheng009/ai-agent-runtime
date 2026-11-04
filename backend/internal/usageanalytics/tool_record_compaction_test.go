package usageanalytics

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactToolRecordsRewritesOversizedPayloads(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	large, err := json.Marshal(map[string]interface{}{
		"tool_call_id":               "call-large",
		"session_id":                 "s-1",
		"output_original_bytes":      120000,
		"output_model_visible_bytes": 2048,
		"summary":                    strings.Repeat("s", 1024),
		"output":                     strings.Repeat("x", 120000),
		"protocol_result":            map[string]interface{}{"content": strings.Repeat("y", 20000)},
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO usage_tool_calls
		(tool_call_id, session_id, tool_name, record_json)
		VALUES ('call-large', 's-1', 'view', ?), ('call-small', 's-1', 'view', '{"summary":"keep"}')`,
		large); err != nil {
		t.Fatalf("seed tool calls: %v", err)
	}

	dry, err := store.CompactToolRecords(context.Background(), ToolRecordCompactOptions{
		MaxBytes:  1024,
		BatchSize: 1,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Rewritten != 1 || dry.BytesAfter >= dry.BytesBefore {
		t.Fatalf("dry-run stats = %+v", dry)
	}
	var original int
	if err := store.db.QueryRow(`SELECT length(record_json) FROM usage_tool_calls WHERE tool_call_id='call-large'`).Scan(&original); err != nil {
		t.Fatalf("read dry-run row: %v", err)
	}
	if original != len(large) {
		t.Fatalf("dry run changed record length = %d, want %d", original, len(large))
	}

	stats, err := store.CompactToolRecords(context.Background(), ToolRecordCompactOptions{
		MaxBytes:  1024,
		BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if stats.Rewritten != 1 || stats.Skipped != 0 || stats.BytesAfter >= stats.BytesBefore {
		t.Fatalf("compact stats = %+v", stats)
	}
	var raw string
	if err := store.db.QueryRow(`SELECT record_json FROM usage_tool_calls WHERE tool_call_id='call-large'`).Scan(&raw); err != nil {
		t.Fatalf("read compacted row: %v", err)
	}
	if strings.Contains(raw, `"output"`) || strings.Contains(raw, `"protocol_result"`) {
		t.Fatalf("compacted record retained payload: %s", raw)
	}
	for _, key := range []string{"tool_call_id", "output_original_bytes", "output_model_visible_bytes", "summary"} {
		if !strings.Contains(raw, `"`+key+`"`) {
			t.Fatalf("compacted record lost %s: %s", key, raw)
		}
	}
}
