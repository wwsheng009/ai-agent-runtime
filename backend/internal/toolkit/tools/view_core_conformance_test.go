package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// viewConformanceFixtures is the text fixture every case below reads.
func viewConformanceFixtures(t *testing.T, root string) {
	t.Helper()
	files := map[string][]byte{
		"small.txt": []byte("one\ntwo\nthree\nfour\n"),
		"empty.txt": nil,
		"long.txt":  []byte(strings.Repeat("x", viewcore.MaxLineChars+10) + "\nafter\n"),
		// ELF magic plus NULs: the binary window classifier must fire.
		"blob.bin": append([]byte{0x7F, 'E', 'L', 'F'}, bytes.Repeat([]byte{0x00}, 128)...),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A BOM'd UTF-16 file exercises the shared codec on both paths.
	utf16 := append([]byte{0xFF, 0xFE}, []byte{'o', 0, 'n', 0, 'e', 0, '\n', 0, 't', 0, 'w', 0, 'o', 0, '\n', 0}...)
	if err := os.WriteFile(filepath.Join(root, "utf16.txt"), utf16, 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory exercises the auto-listing path.
	if err := os.MkdirAll(filepath.Join(root, "subdir", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "subdir", "entry.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Invisible-spelling fixtures: one unique candidate and one ambiguous pair,
	// so both the heal and the "pick one" answer are covered.
	for _, name := range []string{"report\u202Ffinal.txt", "cap\u2000file.txt", "cap\u2001file.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("healed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// viewConformanceDevicePath is a special path the guard must refuse on this
// host (a device on Unix, a reserved DOS name on Windows).
func viewConformanceDevicePath() string {
	if runtime.GOOS == "windows" {
		return "NUL"
	}
	return "/dev/null"
}

// TestViewLocalAndCoreRenderTheSameWindow is the conformance test that makes
// "one tool, two locations" checkable instead of aspirational: the same request
// runs through the local toolkit tool (which owns sandbox, ledger, dedup and
// document/notebook routing) and through the shared viewcore handler (which is
// what the remote surface executes), and both must deliver the same content and
// the same window metadata.
//
// It only covers plain text windows on purpose: those are the paths where the
// two surfaces are supposed to be indistinguishable. Symbol lookup, image
// passthrough and the host read ledger are surface-owned capabilities by design.
func TestViewLocalAndCoreRenderTheSameWindow(t *testing.T) {
	cases := []struct {
		name string
		args map[string]interface{}
	}{
		{"whole small file", map[string]interface{}{"file_path": "small.txt"}},
		{"windowed", map[string]interface{}{"file_path": "small.txt", "offset": 1, "limit": 2}},
		{"tail", map[string]interface{}{"file_path": "small.txt", "offset": -2}},
		{"tail with limit", map[string]interface{}{"file_path": "small.txt", "offset": -3, "limit": 2}},
		{"beyond eof", map[string]interface{}{"file_path": "small.txt", "offset": 99}},
		{"empty file", map[string]interface{}{"file_path": "empty.txt"}},
		{"utf16", map[string]interface{}{"file_path": "utf16.txt"}},
		{"long line", map[string]interface{}{"file_path": "long.txt"}},
		{"binary note", map[string]interface{}{"file_path": "blob.bin"}},
		{"directory listing", map[string]interface{}{"file_path": "subdir"}},
		{"refused device", map[string]interface{}{"file_path": viewConformanceDevicePath()}},
		{"spelling healed", map[string]interface{}{"file_path": "report final.txt"}},
		{"spelling ambiguous", map[string]interface{}{"file_path": "cap file.txt"}},
	}

	// The shared window fields both surfaces must agree on. Local-only stamps
	// (skip_render_truncation, declared budget) are deliberately excluded.
	windowKeys := []string{
		"offset", "limit", "offset_resolved", "lines_read", "total_lines", "eof",
		"is_truncated", "suggested_next_offset", "tail", "tail_lines", "empty",
		"byte_budget_applied", "encoding", "long_lines_truncated", "hidden_bytes",
		"reader_clamped_lines", "reader_clamped_bytes",
		// Path identity: a healed spelling must be reported the same way on both
		// sides, since the next call is supposed to use the resolved path.
		"file_path", "path_auto_healed", "original_path", "resolved_path",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The read-dedup cache is host state the shared handler does not have
			// yet; disabling it keeps this a comparison of the reader, not of the
			// host's memory.
			t.Setenv("AICLI_VIEW_DEDUP", "")
			root := t.TempDir()
			viewConformanceFixtures(t, root)
			ctx := context.Background()

			tool := newViewToolAt(t, root)
			local, err := tool.Execute(ctx, tc.args)
			if err != nil || local == nil {
				t.Fatalf("local view failed: result=%#v err=%v", local, err)
			}

			handler := viewcore.NewHandler(toolcore.Definition{
				Name:                "view",
				Version:             "1.1.0",
				DefaultOutputBudget: viewOutputBudgetBytes,
			})
			core, err := handler.Execute(ctx, &toolcore.Call{
				Args:    tc.args,
				Runtime: newLocalCoreRuntimeWith(ctx, &tool.sandboxPolicy, nil),
			})
			if err != nil || core == nil {
				t.Fatalf("core view failed: result=%#v err=%v", core, err)
			}
			shared := toolkit.ResultToToolkit(core)

			if shared.Success != local.Success {
				t.Fatalf("success diverged: local=%v core=%v", local.Success, shared.Success)
			}
			if shared.Content != local.Content {
				t.Fatalf("content diverged:\n--- local ---\n%s\n--- core ---\n%s", local.Content, shared.Content)
			}
			for _, key := range windowKeys {
				if shared.Metadata[key] != local.Metadata[key] {
					t.Fatalf("metadata[%q] diverged: local=%#v core=%#v\nlocal metadata: %#v\ncore metadata: %#v",
						key, local.Metadata[key], shared.Metadata[key], local.Metadata, shared.Metadata)
				}
			}
		})
	}
}
